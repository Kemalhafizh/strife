# PostgreSQL Replication Under Chaos

This example runs a 3-node PostgreSQL cluster (1 primary, 2 asynchronous replicas)
under network partitions and process pauses, driving read/write/CAS operations
and verifying linearizability and isolation invariants.

## Running the test

1. Boot the cluster:

```sh
docker compose up -d --build
```

2. Inspect the fault schedule:

```sh
strife plan --config strife.json
```

3. Run the live test:

```sh
strife run --config strife.json
```

## What it tests

- Reads routed to asynchronous replicas may observe stale data during partitions (exposing stale reads if client distributes queries across all replicas).
- Acknowledged writes to the primary survive process restarts (`kill -9` followed by `pg_ctl start`).
- Transactional consistency and isolation anomalies under concurrent execution.
