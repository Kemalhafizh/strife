# Example: Valkey primary + replicas under Strife

One Valkey primary (`n1`) with two asynchronous replicas (`n2`, `n3`). Every
Strife client is bound to one node, like an application whose connection pool
spreads reads across replicas:

- clients on the primary read and write it;
- clients on a replica read from it, while their writes are rejected with
  `READONLY` (recorded as definite failures, never as anomalies).

```sh
cd examples/valkey
docker compose up -d --build
```

## 1. Reads from async replicas are not linearizable

```sh
strife run --config strife.json --expect violation
```

This is expected to **fail linearizability**. When a replica is cut off from
the primary it keeps answering reads with whatever it last replicated, while
the primary moves on. Strife reports the stale read with the write it missed
(this is the Valkey/Redis documented behaviour, not a bug).

## 2. A single primary survives `kill -9`

```sh
strife run --config strife-primary.json --expect linearizable
```

All clients use the primary, which is repeatedly killed with SIGKILL and
restarted. With `appendonly yes` the server appends each write to the AOF
with `write()` before it replies, so a process crash loses nothing that was
acknowledged and the history is linearizable. Losing the machine is a
different matter: with `appendfsync everysec` up to a second of acknowledged
writes lives only in the page cache. Try `STRIFE_FSYNC=always` in
`docker-compose.yml` for the strict setting.

## What CI checks

`.github/workflows/live.yml` runs both scenarios on every push, through
Strife's own GitHub Action. Scenario 1 must find a violation and scenario 2
must pass.

```sh
docker compose down -v
```
