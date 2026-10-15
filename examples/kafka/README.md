# Kafka KRaft Cluster Under Chaos

This example runs a 3-node Apache Kafka cluster in KRaft mode (no Zookeeper)
under network partitions and process pauses, driving enqueue (produce) and
dequeue (fetch) operations and verifying queue linearizability with Strife's
built-in `queue` model.

## Running the test

1. Boot the cluster:

```sh
docker compose up -d --build
```

2. Inspect the planned fault schedule:

```sh
strife plan --config strife.json
```

3. Run the live test:

```sh
strife run --config strife.json
```

## What it tests

- **FIFO order guarantees**: Verifies that messages are received in the exact order they were committed.
- **Partition tolerance**: Tests cluster behavior when controller and broker quorums are partitioned.
- **Failover durability**: Tests whether acknowledged produces survive leader crashes (`kill -9`).
