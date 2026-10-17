# Example: TiDB 8.5 under Strife

A single TiDB server with its embedded `unistore` storage: enough to check
TiDB's transaction layer (the SQL front end, its timestamp ordering and its
two transaction modes) without a full PD/TiKV deployment. TiDB speaks the
MySQL protocol, so Strife uses the same client as for MySQL.

```sh
cd examples/tidb
docker compose up -d

strife run --config strife-optimistic.json          # optimistic mode, frozen and thawed
strife run --config strife.json --faults none       # pessimistic mode (TiDB's default)

docker compose down -v
```

## What CI asserts (`tidb` job)

| Config | Transaction mode | Checked as | Faults | Expected |
|---|---|---|---|---|
| `strife-optimistic.json` | optimistic (`init_sql`) | snapshot isolation | `freeze` | pass |
| `strife.json` | pessimistic | snapshot isolation | none | **violation** |

TiDB documents its REPEATABLE READ as snapshot isolation. In optimistic mode
the history agrees. In the default pessimistic mode, writes take locks and
read the latest committed version (like MySQL), so the list-append workload
observes elements committed after the transaction's snapshot: Strife reports
internal-consistency violations and G-single cycles. TiDB documents this
behaviour for pessimistic transactions; the example exists to show that the
checker sees the difference between the two modes.

`db.init_sql` runs on every new connection, here
`SET SESSION tidb_txn_mode = 'optimistic'`.
