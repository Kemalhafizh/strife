# Benchmarks and validation

Every number in Strife's documentation comes from this page, and every number
on this page comes with the command that produced it, the machine it ran on
and the date. Numbers without a reproduction do not belong in the README.

Measured on 2026-10-17 at commit `e5527d8` (CI) and on the working tree of
the same branch (local). Shared CI runners are noisy: treat a single run as a
sample, not a specification. Where two runs differ, both are shown.

## 1. Fault-injection latency

**Question:** when Strife plans a fault for instant *t*, how late does it take
effect?

**Method:** `strife bench inject` applies one fault kind at a time to one node
(rotating through the cluster), at a fixed period, against a 3-node etcd
cluster in Docker, and records for each transition the time from the planned
instant until every involved node confirmed the change. 50 cycles per row
(inject and heal are separate rows), one transition every 300 ms.
For `strife-agent`, "agent-side" is the time inside the agent from receiving
the request until the kernel call returned (slowest node).

```sh
# .github/workflows/live.yml, job "agent"
bin/strife bench inject --config examples/etcd/strife-agent.json \
  --transports docker,agent --faults partition,pause,kill,latency,loss \
  --cycles 50 --period 300ms --markdown bench-inject.md --json bench-inject.json
```

**Host:** GitHub-hosted `ubuntu-24.04`, 4 vCPU AMD EPYC 9V74, Linux
6.17.0-1022-azure, Go 1.23.12. Run
[39039844498](https://github.com/Kemalhafizh/strife/actions/runs/39039844498).

| Transport | Fault | Edge | P50 | P95 | Max | Agent-side P50 / P95 | Errors |
|---|---|---|---|---|---|---|---|
| docker exec | partition | inject | 57.9 ms | 61.3 ms | 63.2 ms | – | 0 / 50 |
| docker exec | partition | heal | 59.4 ms | 64.0 ms | 77.9 ms | – | 0 / 50 |
| docker exec | pause | inject | 43.7 ms | 45.1 ms | 46.3 ms | – | 0 / 50 |
| docker exec | kill | inject | 44.4 ms | 46.1 ms | 46.8 ms | – | 0 / 50 |
| docker exec | latency | inject | 42.8 ms | 44.1 ms | 45.3 ms | – | 0 / 50 |
| docker exec | loss | inject | 42.8 ms | 45.6 ms | 47.2 ms | – | 0 / 50 |
| strife-agent | partition | inject | 4.02 ms | 4.29 ms | 4.41 ms | 2.61 ms / 2.81 ms | 0 / 50 |
| strife-agent | partition | heal | 7.44 ms | 9.58 ms | 10.5 ms | 5.99 ms / 8.53 ms | 0 / 50 |
| strife-agent | pause | inject | 1.36 ms | 1.90 ms | 2.76 ms | 372 µs / 489 µs | 0 / 50 |
| strife-agent | pause | heal | 1.43 ms | 1.96 ms | 2.24 ms | 410 µs / 505 µs | 0 / 50 |
| strife-agent | kill | inject | 1.58 ms | 1.99 ms | 2.04 ms | 495 µs / 707 µs | 0 / 50 |
| strife-agent | kill | heal | 1.41 ms | 1.91 ms | 1.93 ms | 331 µs / 377 µs | 0 / 50 |
| strife-agent | latency | inject | 48.1 ms ¹ | 59.5 ms ¹ | 60.7 ms ¹ | 159 µs / 185 µs | 0 / 50 |
| strife-agent | latency | heal | 1.11 ms | 1.68 ms | 1.72 ms | 158 µs / 189 µs | 0 / 50 |
| strife-agent | loss | inject | 1.23 ms | 235 ms ¹ | 834 ms ¹ | 147 µs / 168 µs | 0 / 50 |
| strife-agent | loss | heal | 1.08 ms | 1.72 ms | 535 ms ¹ | 158 µs / 183 µs | 0 / 50 |

¹ The agent's confirmation leaves through the interface it has just impaired:
with 50 ms ± 10 ms of netem delay the reply itself is delayed by ~50 ms, and
with 20 % loss it sometimes needs a TCP retransmission. The fault took effect
when the kernel call returned (agent-side column); only the confirmation was
late. Docker heal rows (omitted) are within 2 ms of the inject rows.

**Reading it:**

- `docker exec` costs ~43–60 ms per transition, almost all of it in the Docker
  CLI and the exec round trip, with a tight spread (σ ≈ 1–3 ms).
- `strife-agent` lands pause and kill in ~1.4–1.6 ms at P50 and under 3 ms at
  the maximum: ~30× sooner than `docker exec`. Inside the agent the kernel
  work is 330–500 µs for signals and ~150–190 µs for netem changes.
- Partitions through the agent are slower (4–7 ms) because the agent still runs
  the `iptables` binary for them (fork, exec, ruleset reload); netem, signals
  and cgroups are direct syscalls. Native netfilter is listed in the roadmap.
- An earlier run on an AMD EPYC 7763 runner
  ([39038949984](https://github.com/Kemalhafizh/strife/actions/runs/39038949984))
  measured docker 51–76 ms and agent pause/kill 1.40–1.70 ms at P50: the
  ordering is stable, the absolute values move with the runner.

**cgroup faults** (`freeze`, `io-stall`) run on the Docker host, not in the
container, so they do not go through `docker exec`. Job `cgroup`, same run,
AMD EPYC 7763, 20 cycles every 500 ms:

| Fault | Edge | P50 | P95 | Max | Errors |
|---|---|---|---|---|---|
| freeze | inject | 9.56 ms | 10.1 ms | 10.3 ms | 0 / 20 |
| freeze | heal | 9.63 ms | 10.3 ms | 10.5 ms | 0 / 20 |
| io-stall | inject | 8.33 ms | 8.74 ms | 8.95 ms | 0 / 20 |
| io-stall | heal | 8.30 ms | 9.78 ms | 10.3 ms | 0 / 20 |

`freeze` is confirmed only once `cgroup.events` reports `frozen 1`, not when
the write to `cgroup.freeze` returns. The same job checks that `io.max` really
stalls a container's writes (`scripts/live/check-io-stall.sh`).

**Not measured:** the `ssh` and `kubernetes` transports (both shell out per
command, so expect the same order as `docker exec` plus network round trips),
and `clock-skew`.

## 2. Checker throughput

`go test -bench` on the working tree, one core per check (`Parallelism: 1`).
Host: 4 vCPU Intel Xeon @ 2.10 GHz, Linux 6.18, Go 1.24.7.

```sh
go test ./pkg/checker -run '^$' -bench 'Linearizable|Indeterminate|Violation' -benchtime 3x
go test ./pkg/txn     -run '^$' -bench CheckAppend -benchtime 3x
```

**Linearizability (WGL search with memoisation), one register, every
operation on the same key** (the worst case for P-compositionality):

| History | Concurrent processes | Indeterminate writes | Throughput | Steps / op |
|---|---|---|---|---|
| 1,000 ops | 5 | 0 % | 508 k ops/s | 1.4 |
| 10,000 ops | 5 | 0 % | 408 k ops/s | 1.2 |
| 100,000 ops | 5 | 0 % | 477 k ops/s | 1.3 |
| 1,000 ops | 20 | 0 % | 98 k ops/s | 41 |
| 10,000 ops | 20 | 0 % | 107 k ops/s | 33 |
| 100,000 ops | 20 | 0 % | 95 k ops/s | 30 |
| 1,000 ops | 50 | 0 % | 63 k ops/s | 75 |
| 10,000 ops | 50 | 0 % | **unknown** after 50 M steps | – |
| 10,000 ops | 10 | 1 % | 301 k ops/s | 3.0 |
| 10,000 ops | 10 | 5 % | 344 k ops/s | 3.0 |

The last full row is the honest limit: with 50 processes racing on a single
key, the default budget (`MaxSteps` = 50 million per key) runs out and the
verdict is `unknown` (exit code 3), never `linearizable`. Real workloads
spread operations over several keys, which are checked independently.

**Proving a violation:** a stale read is diagnosed directly, without
exhausting the search: 1.1 M ops/s at 1,000 ops, 527 k ops/s at 100,000 ops
(with or without indeterminate writes). The pure search, for comparison,
refutes at ~35 k ops/s.

**List-append (Elle-style dependency graph), strict serializability, 10 keys,
transactions of up to 8 micro-ops:** 41 k txns/s at 1,000 transactions,
57 k txns/s at 10,000, 48 k txns/s at 50,000.

## 3. Checker correctness

Cross-checks against brute force, on small random histories where every
serial order can be enumerated:

```sh
STRIFE_APPEND_ITERS=200000 go test ./pkg/txn -run 'AppendCompleteness' -v
go test ./pkg/txn -run 'AppendAgainstBruteForce|RegisterTxnAgainstBruteForce' -v
```

| Checker | Histories | Valid histories flagged | Invalid histories caught |
|---|---|---|---|
| list-append, strict serializable | 200,000 (150,030 valid) | **0** | 49,877 / 49,970 (**99.81 %**) |
| register transactions, serializable and strict | 40,000 | **0** | (soundness only) |

Soundness (never flagging a history that has a valid order) is required and
enforced by the tests. Completeness cannot be total for list-append: an
anomaly can hide behind an indeterminate transaction, or a corrupted read can
remain explainable by another version order. The 93 misses are of that kind.

Continuous fuzzing (`ci.yml`, job `fuzz`) runs `FuzzCheckAppendSound`,
`FuzzReadJSONL`, `FuzzParseWasm` and the wire-protocol decoders
(`FuzzPostgresMessages`, `FuzzMySQLPackets`, `FuzzKafkaRecords`, `FuzzRESP`).
It found a stack-underflow panic in the Wasm interpreter and an integer
overflow in the MySQL packet decoder, both fixed; their inputs are kept as
regression corpus under `pkg/*/testdata/fuzz`. Every decoder bounds what it
allocates by what actually arrives, not by lengths announced on the wire.

## 4. Bug detection in the simulator

Each row sweeps 200 seeds of a 5-node cluster for 10 simulated seconds
(`--seeds 1..200`). A system with a known bug is only ever reported as
violating; correct Raft never is.

```sh
strife sim --system raft-stale-reads  --faults partition           --seeds 1..200
strife sim --system raft-lease        --faults clock-skew,partition --seeds 1..200
strife sim --system raft-unsafe-fsync --faults kill,io-stall        --seeds 1..200
strife sim --system raft --faults partition,latency,loss,kill,pause,freeze,io-stall,clock-skew --seeds 1..200
```

| System | Bug | Faults | Violating seeds |
|---|---|---|---|
| `raft` | none | all eight kinds | **0 / 200** |
| `raft-stale-reads` | deposed leader serves local reads | partition | 75 / 200 (37.5 %) |
| `raft-lease` | lease timed with the wall clock | clock-skew, partition | 10 / 200 (5 %); 0 / 200 without clock-skew |
| `raft-unsafe-fsync` | acks before `fsync` | kill, io-stall | 7 / 200 (3.5 %); 0 / 200 under partition alone |

The sweeps take 17–34 s each on the 4-vCPU host above.

## 5. Live clusters

Every push runs `.github/workflows/live.yml`. Each row is an assertion: a run
that does not produce the expected verdict fails the job.

| Job | System | Faults | Checked as | Expected and observed |
|---|---|---|---|---|
| etcd | etcd 3.5, 3 nodes | partition, kill, pause, latency ², loss ² | linearizable (kv) | pass |
| etcd | same, transactions | same | strict serializable (list-append) | pass |
| agent | etcd via strife-agent | partition, kill, pause | linearizable | pass |
| cgroup | etcd | freeze, io-stall | linearizable | pass |
| kubernetes | etcd StatefulSet in kind | partition, kill, pause, latency ², loss ² | linearizable | pass |
| valkey | Valkey 8, primary + 2 async replicas | partition, pause | linearizable | **violation** (stale replica reads) |
| valkey | Valkey 8, single primary, `appendfsync always` | kill -9 | linearizable | pass |
| postgres | PostgreSQL 17 primary, SERIALIZABLE | kill -9, pause | strict serializable | pass |
| postgres | PostgreSQL 17, REPEATABLE READ | kill -9, pause | snapshot isolation | pass |
| postgres | same history | – | serializable | **violation** (G2, write skew) |
| postgres | async standbys | partition | strict serializable | **violation** (G-realtime); `shrink --live` reduces it to zero faults |
| mysql | MySQL 8.4, SERIALIZABLE | freeze | strict serializable | pass |
| mysql | MySQL 8.4, REPEATABLE READ | none | snapshot isolation | **violation** (internal, G-single) |
| tidb | TiDB 8.5, optimistic | freeze | snapshot isolation | pass |
| tidb | TiDB 8.5, pessimistic | none | snapshot isolation | **violation** (internal, G-single) |
| kafka | Kafka 3.9.1 and 4.1.0 KRaft | partition, kill -9, pause | strict serializable (list-append) | no lost or reordered records; stale reads (G-realtime) tolerated |

² When the runner kernel has `sch_netem` (it has on every run so far).

Run [39039844498](https://github.com/Kemalhafizh/strife/actions/runs/39039844498)
passed all ten jobs.
