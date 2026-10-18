# Anomalies observed on real systems

Histories recorded by `strife run` against real databases in CI, kept so that
the checkers keep recognising what they found. Each file is a projection of
the recorded history onto the keys the anomaly involves: every transaction
keeps only its micro-operations on those keys. A projection of a valid
history is valid (the same serial order works), and a cycle among the kept
keys survives it, so the file shows the anomaly and nothing else.

## `postgres-17.11-serializable-g2.jsonl`

- **System:** PostgreSQL 17.11 (`postgres:17-bookworm`), one primary,
  `BEGIN ISOLATION LEVEL SERIALIZABLE` in every transaction, list-append
  workload (`INSERT … ON CONFLICT (k) DO UPDATE SET v = v || ',' || …` and
  primary-key `SELECT`s), 10 clients, each on its own connection.
- **Run:** `live` workflow run
  [39056639380](https://github.com/Kemalhafizh/strife/actions/runs/39056639380),
  job `postgres`, `examples/postgres/strife.json`, seed 42, 2026-10-17. The
  nemesis killed and paused the server earlier in the run; none of its
  faults was active during the anomaly, ~9 s after the last one healed.
- **Anomaly:** G2, three transactions that completed within 0.2 ms of each
  other (client-observed at 59.90865, 59.90870 and 59.90885 s):
  - T86 (p43) read `a3.280` without T87's append, then appended to `a2.280`;
  - T87 (p49) appended to `a3.280` and `a4.277`;
  - T90 (p40) appended to `a3.280` after T87, read `a4.277` *with* T87's
    append, and read `a2.280` *without* T86's.

  T86 must precede T87, T87 must precede T90, and T90 must precede T86: no
  serial order exists. Every edge comes straight from what a transaction
  read, so the cycle does not depend on how the checker infers version
  orders. At snapshot isolation the history passes (write skew is allowed
  there); at serializable it does not.
- **Replaying it:** the same statements issued one at a time by three `psql`
  sessions, in each order consistent with the reads, are aborted correctly
  (`Canceled on identification as a pivot` or `Canceled on conflict out to
  pivot`), so the anomaly needs the commits to race. It has appeared once in
  this job's CI runs so far.

```sh
strife check --model append --isolation serializable testdata/anomalies/postgres-17.11-serializable-g2.jsonl        # violation (G2)
strife check --model append --isolation snapshot-isolation testdata/anomalies/postgres-17.11-serializable-g2.jsonl  # passes
```
