# Example: MySQL 8.4 under Strife

One MySQL 8.4 server with its default authentication, `caching_sha2_password`
(Strife implements the fast path, and the full exchange over TLS or with the
server's RSA key). Workloads are list-append transactions; each list is a
comma-separated column grown with `INSERT ... ON DUPLICATE KEY UPDATE`.

```sh
export STRIFE_MYSQL_PASSWORD=$(openssl rand -hex 16)
cd examples/mysql
docker compose up -d

# SERIALIZABLE, checked as strict serializable, while the server's cgroup is
# frozen and thawed (needs root or passwordless sudo on the docker host):
strife run --config strife.json

# REPEATABLE READ, checked as snapshot isolation, no faults:
strife run --config strife-repeatable-read.json

docker compose down -v
```

## What CI asserts (`mysql` job)

| Config | Isolation | Checked as | Faults | Expected |
|---|---|---|---|---|
| `strife.json` | `SERIALIZABLE` | strict serializable | `freeze` | pass |
| `strife-repeatable-read.json` | `REPEATABLE READ` | snapshot isolation | none | **violation** |

MySQL's REPEATABLE READ is not snapshot isolation: reads come from the
transaction's snapshot, but writes act on the latest committed row. A
transaction that appends to a list therefore sees, in its next read, elements
other transactions committed after its snapshot was taken. A 30-second run
against `mysql:8.4` found 127 internal-consistency violations and 16
G-single cycles (read skew), with no fault injected. This matches what
[Jepsen reported for MySQL 8.0.34](https://jepsen.io/analyses/mysql-8.0.34).

`"cgroup": "auto"` asks the docker transport to find the container's cgroup
when the run starts, so the config stays portable across hosts.
