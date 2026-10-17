# Example: PostgreSQL 17 under Strife

A primary (`n1`) and two asynchronous hot standbys (`n2`, `n3`), with
SCRAM-SHA-256 authentication and TLS. Strife's client speaks the v3 wire
protocol itself (no libpq), runs list-append transactions, and the checker
reconstructs the dependency graph from what each transaction read.

```sh
export STRIFE_PG_PASSWORD=$(openssl rand -hex 16)
cd examples/postgres
docker compose up -d --build

strife run --config strife.json            # primary, SERIALIZABLE, kill -9 + pause
strife run --config strife-snapshot.json   # primary, REPEATABLE READ
strife run --config strife-replicas.json   # reads spread across the standbys

docker compose down -v
```

The password never appears in a config: `"password": "env:STRIFE_PG_PASSWORD"`
is resolved at run time and reports show the reference, not the value.

## What CI asserts (`postgres` job in `.github/workflows/live.yml`)

| Config | Isolation asked of PostgreSQL | Checked as | Faults | Expected |
|---|---|---|---|---|
| `strife.json` | `SERIALIZABLE` | strict serializable | `kill`, `pause` | pass |
| `strife-snapshot.json` | `REPEATABLE READ` | snapshot isolation | `kill`, `pause` | pass |
| same history | | serializable | | **violation**: write skew (G2) |
| `strife-replicas.json` | `REPEATABLE READ` per standby | strict serializable | `partition` | **violation**: stale reads (G-realtime) |

The first row has failed once: in one run, three `SERIALIZABLE`
transactions whose commits raced within 0.2 ms formed an anti-dependency
cycle (G2) that PostgreSQL 17.11 should have aborted. The history and the
reasoning are in [`testdata/anomalies`](../../testdata/anomalies/README.md).
If this job fails with a G2, keep the run's artifact: it is a finding, not a
flake.

The last row is not a PostgreSQL bug: asynchronous standbys replay the WAL
behind the primary, so a client reading from one can miss a transaction that
already committed. `strife shrink --live` confirms it needs no fault at all:
the minimal schedule is empty and still reproduces in every verification run.

```sh
strife shrink --live --config strife-replicas.json --duration 15s --attempts 2 --verify 3
```

## Knobs

- `db.tls`: `require` (encrypt, do not verify), `verify-full` with `db.tls_ca`
  for a CA bundle, or omit for plaintext.
- `db.txn_isolation` sets the level PostgreSQL runs at (`BEGIN ISOLATION LEVEL
  ...`); `workload.isolation` is what the checker holds it to. Keeping them
  apart is how the snapshot example shows the gap between the two.
- Ambiguous errors (connection lost after `COMMIT` was sent) are recorded as
  indeterminate; serialization failures (`40001`), deadlocks (`40P01`) and
  read-only standby errors (`25006`) as definite failures.
