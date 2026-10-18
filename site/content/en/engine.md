---
title: Under the hood
description: A deterministic simulator, linearizability and transaction-isolation checkers, dependency-free database clients, kernel-level fault injection and auditable artifacts.
eyebrow: For engineers
lead: Strife is written in Go using only the standard library. In the simulator a 64-bit seed fixes the whole execution; on a real cluster it fixes the fault schedule and every client's operation stream.
translationKey: engine
---

## Two ways to test

**The deterministic simulator** (`strife sim`). A complete Raft cluster runs inside one process, FoundationDB style. The network, timers, disk flushes and clock drift all follow the seed, so an execution replays bit for bit and is fingerprinted with a SHA-256 digest. Ten simulated seconds take a few hundred milliseconds, so a laptop can sweep thousands of fault schedules. Try it in the [playground](/try).

**Real clusters** (`strife run`). The CLI drives clients against etcd, PostgreSQL, MySQL/TiDB, Valkey or Kafka and injects faults over Docker, SSH, Kubernetes or `strife-agent`. The history is checked by the same checkers. See [Live Clusters](/docs/live-clusters).

## Faults it injects

| Fault | Mechanism |
| :--- | :--- |
| `partition` | iptables `DROP` between two sides of the cluster |
| `latency`, `loss` | `tc netem` (through the agent: netlink packets directly) |
| `kill` | `SIGKILL`, restarted when the fault ends |
| `pause` | `SIGSTOP` / `SIGCONT` |
| `freeze` | cgroup v2 freezer |
| `io-stall` | cgroup v2 `io.max` throttled to almost nothing, so `fsync` blocks |
| `clock-skew` | clock rate and wall-clock jumps |

Through `strife-agent` the median delay of a pause is 1.36 ms, against 43.7 ms through `docker exec` ([method and machine](/docs/benchmarks#1-fault-injection-latency)).

## Checkers

| Model | What it proves |
| :--- | :--- |
| `kv` | Linearizability of read/write/CAS registers (Wing & Gong / Lowe search with memoisation, per-key P-compositionality) |
| `set`, `queue`, `counter`, `idgen` | Linearizability of sets, FIFO queues, counters and unique-ID generators |
| `txn` | Transaction isolation through a Direct Serialization Graph: Adya's G0, G1a, G1b, G1c, G2 and G-realtime across six isolation levels |
| `append` | Elle-style list-append: G0–G2, G-single, G-realtime, lost appends, incompatible orders, duplicates and internal consistency |

When the search budget runs out the verdict is **unknown**, never a pass. Checker speed and accuracy are measured in the [benchmarks](/docs/benchmarks#2-checker-throughput), limits included; each model is described in [Consistency Models](/docs/models).

## Dependency-free database clients

Every protocol is implemented in the repository: etcd v3, Redis/Valkey (RESP2, AUTH, TLS), PostgreSQL (SCRAM-SHA-256, TLS), MySQL/TiDB (`caching_sha2_password`, TLS) and Kafka (Produce v3, Fetch v4, RecordBatch v2). Credentials are given as `env:` or `file:` references and never reach a report.

## Shrinking failures

`strife shrink` uses delta debugging to reduce a violation to a **1-minimal** scenario: dropping any one remaining fault or operation makes the violation disappear. For real clusters the result is called a *candidate*, with its measured reproduction rate. See [Shrinking](/docs/shrinking).

## Auditable artifacts

Every run writes `report.html` (an interactive timeline that works offline), `report.json`, `history.jsonl` and a replayable `schedule.json`. Real examples:

- [Timeline report](/demos/timeline.html): `raft-unsafe-fsync` seed 13, shrunk
- [Regression campaign dashboard](/demos/campaign.html): 40 seeds
- [Compliance dossier](/demos/dossier.html): DORA, SOC 2, ISO 27001 and OJK

## WebAssembly workloads

Custom workloads can be written as WebAssembly 1.0 modules, run by a built-in interpreter with fuel, stack and memory limits, so they stay deterministic and cannot loop forever. See [Custom Workloads](/docs/workloads).
