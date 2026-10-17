# Example: Kafka (KRaft) under Strife

Three Kafka brokers in KRaft mode (combined broker and controller), built from
the Apache release tarball (`KAFKA_VERSION`, default 3.9.1; CI also runs
4.1.0). Strife's client speaks the Kafka protocol directly: ApiVersions,
Metadata v4, Produce v3 with `acks=all`, and Fetch v4 of RecordBatch v2
(CRC-32C checked).

Each Strife key is a single-partition topic, treated as an append-only list:
a produce appends one element, a read fetches the whole log from offset 0.
The list-append checker then derives the order of the log from the reads.

```sh
cd examples/kafka
KAFKA_VERSION=4.1.0 docker compose up -d --build
strife run --config strife.json      # partition, kill -9, pause
docker compose down -v
```

## What CI asserts (`kafka` job, `scripts/live/check-kafka.py`)

- **No acknowledged record is lost** (`lost-append`), and logs never diverge,
  reorder or duplicate records (`incompatible-order`, `duplicate-elements`).
  Every key with an acknowledged produce must have a successful final read.
- **Stale reads are tolerated.** Kafka does not promise linearizable reads:
  after a leader change, a fetch can be served by a new leader whose high
  watermark has not yet caught up, or by a deposed leader that has not yet
  learned it was deposed. Both versions show this as G-realtime cycles.

The run is checked at strict serializability so that those stale reads are
visible in the report instead of silently allowed.
