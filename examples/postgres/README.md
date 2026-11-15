# Example: PostgreSQL 17 under Strife

> **Language:** English · [Bahasa Indonesia](../../docs/id/examples/postgres/README.md)

A primary (`n1`) and two asynchronous hot standbys (`n2`, `n3`), with
SCRAM-SHA-256 authentication and TLS. Strife's client speaks the v3 wire
protocol itself (no libpq), runs list-append, double-entry ledger, or
settlement transactions, and checks the recorded execution history.

```sh
export STRIFE_PG_PASSWORD=$(openssl rand -hex 16)
cd examples/postgres
docker compose up -d --build

strife run --config strife.json --expect no-anomaly          # primary, SERIALIZABLE list-append, kill -9 + pause
strife run --config strife-ledger.json --expect no-anomaly   # primary, SERIALIZABLE double-entry ledger audit
strife run --config strife-snapshot.json --expect no-anomaly # primary, checked as snapshot isolation
strife run --config strife-replicas.json --expect violation  # reads spread across the standbys

docker compose down -v
```

The password never appears in a config: `"password": "env:STRIFE_PG_PASSWORD"`
is resolved at run time and reports show the reference, not the value.

`strife-settlement.json` is an additional two-leg `settlement` workload template
configured with the zero-privilege `proxy` transport against a standalone
PostgreSQL cluster (`172.30.0.11:5432`, user/database `strife`); adjust
`nodes` and `db.endpoints` if running it against the local compose network, and
pass `--expect no-anomaly` in CI (because the transactional checkers for `append`,
`ledger`, and `settlement` report `unknown (sound incomplete)` rather than a
complete linearizability proof when a history exceeds bounded complete search).

## What CI asserts (`postgres` job in `.github/workflows/live.yml`)

| Config | Isolation asked of PostgreSQL | Checked as | Faults | Expected |
|---|---|---|---|---|
| `strife.json` | `SERIALIZABLE` | strict serializable (`append`) | `kill`, `pause` | no anomaly (`--expect no-anomaly`) |
| `strife-ledger.json` | `SERIALIZABLE` | strict serializable (`ledger`) | `kill`, `pause` | no anomaly (`--expect no-anomaly`) |
| `strife-snapshot.json` | default | snapshot isolation (`append`) | `kill`, `pause` | no anomaly (`--expect no-anomaly`) |
| same history | | serializable | | **violation**: write skew (G2) |
| `strife-replicas.json` | `REPEATABLE READ` per standby | strict serializable (`append`) | `partition` | **violation**: stale reads (G-realtime) |

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
