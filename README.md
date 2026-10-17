<div align="center">

# Strife

**Deterministic fault injection, linearizability verification, and transactional anomaly detection for distributed systems.**

[![CI](https://github.com/Kemalhafizh/strife/actions/workflows/ci.yml/badge.svg)](https://github.com/Kemalhafizh/strife/actions/workflows/ci.yml)
[![Live Clusters](https://github.com/Kemalhafizh/strife/actions/workflows/live.yml/badge.svg)](https://github.com/Kemalhafizh/strife/actions/workflows/live.yml)
[![Go 1.23+](https://img.shields.io/badge/go-1.23%2B-00ADD8?logo=go&logoColor=white)](go.mod)
[![Dependencies](https://img.shields.io/badge/dependencies-zero%20(stdlib%20only)-2ea44f)](go.mod)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

[Quick Start](#quick-start) · [Interactive Demos](site/index.html) · [Shrinking](#shrinking-failures) · [Models & Checkers](#consistency-models--anomaly-detection) · [Live Clusters & Agent](#live-clusters--strife-agent) · [Wasm Workloads](#custom-workloads--wasm-vm) · [Campaigns](#regression-campaigns) · [Attestation & Compliance](#cryptographic-attestation--compliance-dossiers) · [Architecture](#architecture)

</div>

---

Strife drives concurrent workloads against a simulated or live cluster while injecting network partitions, packet delay and loss, process crashes (`SIGKILL`), process pauses (`SIGSTOP`), cgroup v2 freezes, block I/O stalls, and clock skew. It records every client invocation and completion in a Jepsen-style history and proves whether the execution satisfies linearizability or transactional isolation.

Every run is controlled by a 64-bit seed. On live clusters, the seed fixes the fault schedule and each client's operation stream. Inside the built-in discrete-event simulator, the seed fixes the entire execution—network delivery order, timer firings, disk flushes, and clock drift—replaying bit-for-bit with a verifiable SHA-256 trace digest.

```console
$ strife sim --system raft-unsafe-fsync --faults kill,io-stall --seed 13
system:  raft-unsafe-fsync   seed: 13   nodes: 5   simulated: 10s   events: 108793   wall: 521ms
faults:  6 scheduled (schedule 935d69e22cc1a0f3)
           1.056s →   2.127s  kill [0 2]
           2.668s →   4.069s  io-stall [3]
           4.070s →   6.177s  kill [1]
           4.714s →   5.654s  io-stall [0 1 3]
           6.507s →   8.500s  io-stall [0 1 2 3 4]
           7.878s →   8.500s  kill [0 1 2 3 4]
ops:     4429 (3717 ok, 692 fail, 20 info)   history 66a5f95de89d72c8   trace 50173cb8d89db240
verdict: VIOLATION
  key k0: No linearization places "p29 read k0 → 3" before it returned. After the longest
  valid prefix (854 ops) the model state is 1, and none of the 2 concurrent candidates can
  be applied first to reach a state that explains it.
```

In the trace above, a Raft implementation acknowledges log appends before `fsync` completes. When all five disks stall at `6.507s` and the cluster suffers a simultaneous power loss at `7.878s`, a committed write disappears on recovery. Because ten simulated seconds execute in ~520ms of wall time, a single workstation can sweep thousands of fault schedules in a few minutes.

---

## Design Constraints

- **Zero external dependencies.** Both `strife` and `strife-agent` compile from the Go 1.23 standard library alone (`go.mod` has no `require` block). Wire protocols for etcd v3, Redis/Valkey (RESP2), PostgreSQL (v3.0), Kafka (v0), MySQL/TiDB (v10), Linux `AF_NETLINK` route sockets, Ed25519 `in-toto` signing, and the WebAssembly 1.0 bytecode interpreter are implemented in-tree.
- **Pinned randomness.** Standard `math/rand` algorithms are not guaranteed to stay identical across Go releases. Strife uses a self-contained `xoshiro256**` PRNG seeded via `SplitMix64` ([`internal/rng`](internal/rng/rng.go)) with per-actor FNV-1a stream forking (`root.Fork("client/3")`), so adding a client or node does not perturb another actor's random draws.
- **Honest verdicts.** When a checker exhausts its step or wall-clock budget without finding either a valid linearization or a proof of impossibility, the verdict is `unknown` (exit code `3`), never a false `linearizable`.

---

## Quick Start

```sh
# Build the CLI (requires Go 1.23+, no CGo)
go build -o strife ./cmd/strife

# List built-in simulated consensus implementations
strife systems

# Run a single seed of correct Raft under all eight fault kinds
strife sim --system raft --seed 7

# Sweep 100 seeds against a Raft variant that serves local leader reads without a quorum round
strife sim --system raft-stale-reads --faults partition --seeds 1..100

# Verify bit-for-bit simulator determinism across 500 seeds in CI
strife sim --system raft --seeds 1..500 --determinism --expect linearizable
```

Each violating run writes a self-contained artifact directory (`strife-out/<system>-seed<N>/`) containing:

| Artifact | Purpose |
| :--- | :--- |
| `report.html` | Offline HTML5/Canvas timeline studio (60fps pan/zoom, anomaly auto-focus, key/process filters, operation inspector, `<noscript>` SVG fallback) |
| `report.md` | Markdown summary suitable for pull requests and `$GITHUB_STEP_SUMMARY` |
| `report.json` | Machine-readable verdict, counterexample witnesses, root-cause findings, and SHA-256 fingerprint |
| `history.jsonl` | Complete operation log (`invoke`, `ok`, `fail`, `info`) with nanosecond timestamps |
| `schedule.json` | Replayable fault schedule (`strife sim --plan schedule.json`) |

---

## Shrinking Failures

A raw violation often spans thousands of operations and half a dozen overlapping faults. `strife shrink` reduces both simulated scenarios and recorded histories to a **1-minimal** counterexample—meaning that dropping any single remaining fault or operation makes the violation disappear.

### 1. Scenario Shrinking (`strife shrink`)

Delta debugging (`ddmin`) runs across nine deterministic passes: test duration, fault set, fault node targets, fault start/end windows, timing grid snapping (`100ms` → `10ms` → `1ms`), fault magnitudes, client count, a second duration pass, and a final 1-minimality check.

```console
$ strife shrink --system raft-unsafe-fsync --faults kill,io-stall --seed 13
shrinking raft-unsafe-fsync seed 13…
  baseline    1 runs  faults 6   10s       clients 10  6 faults, 4429 ops
  duration   18 runs  faults 6   8.94s     clients 10  10s → 8.94s
  faults     27 runs  faults 2   8.94s     clients 10  ddmin: 6 → 2 faults
  targets    46 runs  faults 2   8.94s     clients 10  removed 4 fault targets
  windows    86 runs  faults 2   8.94s     clients 10  total fault time now 1.52s
  round      98 runs  faults 2   8.94s     clients 10  snapped 3 fault boundaries to a coarse grid; 1 timing-critical boundaries kept exact
  verify    126 runs  faults 2   8.39s     clients 10  1-minimal: every remaining fault [4 5] is necessary
minimal: 6 → 2 faults, 10s → 8.39s, 10 → 10 clients, 4429 → 3931 ops (127 runs, 18s)
         #4   6.5s          → 7.9s          io-stall [3]
         #5   7.877832081s  → 8.1s          kill [0 1 2 3 4]
```

Notice `#5`: the `kill` timestamp (`7.877832081s`) was kept unrounded because snapping it to a coarse grid allowed the stalled `fsync` on node 3 to finish before power loss. Across the benchmark corpus in [`docs/ROADMAP.md`](docs/ROADMAP.md), **35 of 37 violating seeds (95%)** reduce to $\le 3$ faults in 80–150 simulator runs.

### 2. History Shrinking (`strife shrink --history`)

For live cluster traces where re-running the cluster is probabilistic, `strife shrink --history` reduces a recorded `history.jsonl` offline:

```console
$ strife shrink --history ops.jsonl --model kv --out strife-out/
shrinking history "ops.jsonl" (2400 ops, model kv)…
  partition    150 ops   isolated violating key "k0"
  diagnose     3 ops     found sound register witness
  ddmin-read   3 ops     eliminated non-essential reads
  ddmin-sound  3 ops     preserved causal write history (isSoundHistory invariant)
  verify       3 ops     1-minimal: dropping any remaining operation removes violation
minimal: 2400 → 3 ops (0.04s)
         #1  call 0.001s   return 0.005s   p1 write k0 → 2
         #2  call 0.006s   return 0.008s   p2 write k0 → 3
         #3  call 0.009s   return 0.012s   p3 read k0 → 2
diagnosis: stale read: p3 read 2 returned at 0.012s, but p2 write 3 completed earlier at 0.008s (1.000ms before read call)
```

> [!IMPORTANT]
> Naive operation dropping can delete the write that produced a value later observed by a read, fabricating a false "unwritten value" violation. `pkg/shrink` enforces an `isSoundHistory` invariant: a write (or `info` write) that supplies the value for a retained read cannot be removed unless the dependent read is removed first.

---

## Consistency Models & Anomaly Detection

`strife check` verifies any JSONL history against six built-in models:

```sh
strife check --history ops.jsonl --model kv --out report/
strife check --history txn.jsonl --model txn --isolation snapshot-isolation
```

| Model | Operations | Verification Engine & Complexity |
| :--- | :--- | :--- |
| `kv` | `read`, `write`, `cas` | Wing & Gong / Lowe linearizability search with bitset state memoization, per-key P-compositionality, symmetry reduction, and $O(N \log N)$ witness extraction (`120k–260k ops/s` per core). |
| `set` | `add`, `read` | Grow-only set linearizability + missing-element counterexample witnesses. |
| `queue` | `enqueue`, `dequeue` | FIFO queue linearizability; diagnoses phantom dequeues, FIFO order inversions, and premature empty returns. |
| `counter` | `inc` / `add`, `read` | Shared integer counter linearizability; diagnoses lost increments and overstated reads. |
| `idgen` | `generate` | Unique monotonic/non-colliding ID verification with duplicate witness extraction. |
| `txn` | `txn` (`[r(k), w(k,v), ...]`) | Direct Serialization Graph (DSG) construction (`wr`, `ww`, `rw`, `so`, `ro` edges) + Tarjan SCC shortest-cycle extraction across 6 isolation levels. |

<details>
<summary><strong>Transactional Isolation Levels & Adya Anomaly Classification (<code>--model txn</code>)</strong></summary>

<br>

The transactional checker ([`pkg/txn`](pkg/txn/txn.go)) reconstructs item versions from write micro-operations and checks for forbidden phenomena defined by Adya (1999):

- **`G0` (Dirty Write):** Cycle consisting solely of write-write (`ww`) dependency edges.
- **`G1a` (Aborted Read):** A committed transaction reads a value written by an aborted/failed transaction.
- **`G1b` (Intermediate Read):** A committed transaction reads a non-final intermediate write of another transaction.
- **`G1c` (Circular Information Flow):** Directed cycle of `wr` and `ww` edges (forbidden by `read-committed` and stronger).
- **`G2-item` / `G2` (Anti-Dependency Cycles):** Cycles containing one or more read-write (`rw`) anti-dependency edges, such as Write Skew (`T1` reads `x`, `T2` reads `y`, `T1` writes `y`, `T2` writes `x`).
- **`G-realtime` (Real-Time Inversion):** Serialization order contradicts wall-clock completion order (`ro` edge), violating `strict-serializable`.

Supported `--isolation` flags: `read-uncommitted`, `read-committed`, `repeatable-read`, `snapshot-isolation`, `serializable`, `strict-serializable`.

</details>

---

## Live Clusters & `strife-agent`

Strife tests live clusters over three transports (`docker`, `ssh`, and `agent`) against five built-in wire-protocol clients:

| Target (`db.type`) | Wire Protocol | Operations Supported | Error Classification (`ok` / `fail` / `info`) |
| :--- | :--- | :--- | :--- |
| `etcd` | etcd v3 HTTP/JSON gateway ([`pkg/client/etcd.go`](pkg/client/etcd.go)) | `read`, `write`, `cas` (`Txn` compare) | HTTP 4xx / connection refused → `fail`; write timeout or broken pipe → `info` |
| `redis` | RESP2 ([`pkg/client/redis.go`](pkg/client/redis.go)) | `GET`, `SET`, atomic Lua `EVAL` `cas` | `-READONLY` / `-ERR` → `fail`; socket drop after write send → `info` |
| `postgres` | PostgreSQL v3.0 wire ([`pkg/client/postgres.go`](pkg/client/postgres.go)) | `kv`, `queue`, `counter`, multi-op `txn` | SQLSTATE `40001` / `40P01` / `23505` → `fail`; commit/write I/O drop → `info` |
| `kafka` | Kafka binary v0 ([`pkg/client/kafka.go`](pkg/client/kafka.go)) | `enqueue` (Produce v0), `dequeue` (Fetch v0) | Broker error codes (`NotLeaderForPartition`, etc.) → `fail`; produce timeout → `info` |
| `mysql` / `tidb` | MySQL v10 + Native 4.1 auth ([`pkg/client/mysql.go`](pkg/client/mysql.go)) | `kv`, `queue` (`FOR UPDATE`), `counter`, `txn` | Error `1213` / `1205` / TiDB `9007` (`WriteConflict`) → `fail`; write drop → `info` |

> [!NOTE]
> Whenever a client records `info` (for example, a TCP connection reset after sending a `SET` or `COMMIT`), the operation may still take effect on the server at an arbitrary point in the future. Following Jepsen's process invariant, the worker closes the socket and increments its logical `process` ID (`process += concurrency`) before issuing another operation.

### Sub-Millisecond Injection via `strife-agent`

Shell-based fault injection (`docker exec` or `ssh` invoking `tc`, `iptables`, and `pkill`) suffers 45–120ms of fork/handshake jitter. `cmd/strife-agent` runs inside each node and applies faults directly via Linux `AF_NETLINK` (`RTM_NEWQDISC` / `RTM_DELQDISC` for `sch_netem`), direct `/proc` scanning + `syscall.Kill`, and cgroup v2 (`cgroup.freeze`, `io.max`):

| Transport | Mechanism | Median Latency (P50) | 95th Percentile (P95) | Max Jitter |
| :--- | :--- | ---: | ---: | ---: |
| `docker` | `docker exec` + `/bin/sh` | ~45.0 ms | ~85.0 ms | > 200 ms |
| `ssh` | OpenSSH session + remote shell | ~60.0 ms | ~120.0 ms | > 350 ms |
| **`agent`** | **Persistent socket + Linux `RTNETLINK`** | **0.28 ms** | **0.95 ms** | **< 3.0 ms** |

### Server-Side Diagnostic Probes & Root-Cause Correlation

While client histories prove *whether* a consistency violation occurred, `strife-agent` also exposes `GET /probes` ([`pkg/probe`](pkg/probe/probe.go)) to explain *why* it occurred without instrumenting application code:

- **Storage `fsync` canary:** Measures active `fsync` latency stalls and `EIO`/`ENOSPC` errors on the database volume.
- **Linux Pressure Stall Information (PSI):** Reads `/proc/pressure/io` and `/proc/pressure/cpu` microsecond stall deltas (`io_pressure`, `cpu_pressure`).
- **cgroup v2 OOM & Process Signals:** Tracks `oom_kill` events in `memory.events` and process signals (`SIGKILL`, `SIGSTOP`, `CGROUP_FREEZE`).
- **Clock Jumps:** Detects non-monotonic wall-clock leaps (`clock_jump`).

During `strife run` (or offline via `strife check --history ops.jsonl --probes probes.jsonl`), the correlator ([`pkg/probe/correlate.go`](pkg/probe/correlate.go)) matches counterexample windows and indeterminate `info` writes against overlapping node telemetry, attaching ranked root-cause attributions (`high` / `medium` / `low` confidence) directly to `report.json` and `report.md`.

> [!WARNING]
> Strife refuses `clock-skew` faults when `transport.type` is `"docker"`. Containers share the host kernel clock (`CLOCK_REALTIME`), so stepping the clock inside a container would skew the Docker host itself. Use the simulator or VM-isolated nodes (`ssh` / `agent`) for clock-skew testing.

---

## Custom Workloads & Wasm VM

Strife includes a zero-dependency, fuel-metered **WebAssembly 1.0 (MVP) bytecode interpreter** ([`pkg/workload/wasm.go`](pkg/workload/wasm.go)) and a declarative-to-Wasm compiler ([`pkg/workload/workload.go`](pkg/workload/workload.go)). Custom workloads can be supplied as `.wasm` binaries, `.json` specifications, or built-in `preset:<name>` profiles.

```sh
# List built-in workload presets and their compiled Wasm sizes
strife workload --list

# Compile a hotspot CAS preset (or custom .json spec) into a standalone .wasm binary
strife workload --compile preset:cas-hotspot --out cas-hotspot.wasm

# Validate a .wasm plugin, print its SHA-256 digest, and preview deterministic ops
strife workload --inspect cas-hotspot.wasm --seed 42 --sample 6

# Run a deterministic simulation or multi-seed campaign with the .wasm workload
strife sim --system raft --workload cas-hotspot.wasm --seed 7 --determinism
strife campaign --system raft --workload preset:read-heavy --seeds 1..100
```

| Built-in Preset | Distribution | Operation Mix | Primary Stress Target |
| :--- | :--- | :--- | :--- |
| `preset:kv-balanced` | `uniform` | 50% `read`, 30% `write`, 20% `cas` | General register linearizability |
| `preset:read-heavy` | `uniform` | 85% `read`, 10% `write`, 5% `cas` | Stale leader-lease reads (YCSB-B) |
| `preset:write-heavy` | `uniform` | 20% `read`, 60% `write`, 20% `cas` | Log replication & `fsync` durability (YCSB-A) |
| `preset:cas-hotspot` | `hotspot` (80% on 20% keys) | 25% `read`, 25% `write`, 50% `cas` | Lock contention & compare-and-swap races |
| `preset:sequential-scan` | `sequential` | 70% `read`, 30% `write` | Deterministic key-range traversal |
| `preset:si-write-skew` | `hotspot` (85% on 25% keys) | 100% multi-key `txn` (`r, r, w`) | Snapshot Isolation write-skew (`G2`) |

<details>
<summary><strong>WebAssembly 1.0 Plugin ABI & Sandbox Guarantees</strong></summary>

<br>

A `.wasm` workload module exports a single pure function:

```wat
(func (export "next_op")
  (param $process i32)   ;; logical client process ID
  (param $step i32)      ;; monotonic per-process step counter (0, 1, 2, ...)
  (param $rand_u64 i64)  ;; 64-bit draw from the process's forked xoshiro256** stream
  (param $keys i32)      ;; configured key domain size
  (param $values i32)    ;; configured value domain size
  (result i64))          ;; bit-packed operation descriptor
```

**Return Value Bit Layout (`uint64`):**
- **Bits `0..7`:** Operation code (`0`=read, `1`=write, `2`=cas, `3`=add, `4`=enqueue, `5`=dequeue, `6`=inc, `7`=generate, `8`=txn).
- **Bits `8..23`:** Key index (`uint16`, taken modulo `keys`).
- **Bits `24..43`:** Primary value (`20-bit` unsigned integer, taken modulo `values`).
- **Bits `44..63`:** Auxiliary / expected CAS value (`20-bit` unsigned integer, taken modulo `values`).

**Sandbox Guarantees:**
- Every invocation runs in an isolated stack/memory frame with a default instruction budget of `10,000` instructions (`DefaultMaxFuel`) and a call-stack depth limit of `32`. Infinite loops trap immediately without hanging the simulator.
- Linear memory is capped at 16 pages (`1 MiB`) with bounds-checked loads and stores.

</details>

---

## Regression Campaigns

`strife campaign` executes parallel seed sweeps, writes an interactive regression dashboard (`campaign.html`), and compares results against a committed baseline manifest (`baseline.json`) to catch regressions across commits:

```sh
# 1. Establish a baseline on main
strife campaign --system raft --seeds 1..200 --commit $(git rev-parse --short HEAD) \
  --save-baseline baseline.json --out strife-campaign

# 2. Gate a pull request against regressions (automatically replays any previously failing seeds)
strife campaign --system raft --seeds 1..200 --baseline baseline.json \
  --expect no-regression --junit junit.xml --summary "$GITHUB_STEP_SUMMARY"
```

The diff engine ([`pkg/campaign/diff.go`](pkg/campaign/diff.go)) classifies every seed into `regressed` (`linearizable → violation`), `fixed` (`violation → linearizable`), `persistent_failure`, `new_failure`, or `trace_drift` (verdict unchanged, but simulator `trace_hash` diverged).

---

## Cryptographic Attestation & Compliance Dossiers

For regulated environments (banking, fintech, and storage vendors), Strife provides offline Ed25519 signing ([`pkg/attest`](pkg/attest/attest.go)) and automated technical control mapping ([`pkg/compliance`](pkg/compliance/compliance.go)).

```sh
# 1. Generate an Ed25519 keypair (PKCS#8 private .key mode 0600, PKIX public .pub)
strife keygen --out audit-key

# 2. Sign a test report into an in-toto v0.1 Statement bundle
strife sign --report strife-out/raft-seed7/report.json \
  --key audit-key.key --signer "release-pipeline" --out strife-out/raft-seed7/bundle.json

# 3. Verify the signature, recomputed report fingerprint, and raw history.jsonl digest
strife verify --bundle strife-out/raft-seed7/bundle.json \
  --key audit-key.pub --history strife-out/raft-seed7/history.jsonl

# 4. Generate a regulatory compliance dossier (JSON, Markdown, and print-ready HTML)
strife compliance --report strife-out/raft-seed7/report.json \
  --bundle strife-out/raft-seed7/bundle.json --key audit-key.pub \
  --frameworks dora,soc2,iso27001,bi-ojk --out strife-compliance --strict
```

### Supported Regulatory Frameworks

| Framework | Flag | Mapped Technical Controls |
| :--- | :--- | :--- |
| **EU DORA** (Regulation 2022/2554) | `dora` | `DORA-Art.24-25` (Operational Resilience Testing), `DORA-Art.9.4(c)` (Data Integrity Under Disruption), `DORA-Art.11-12` (Crash Recovery & Storage Durability) |
| **AICPA SOC 2** (Trust Services Criteria) | `soc2` | `PI1.3-PI1.4` (Processing Integrity & Completeness), `A1.2` (Environmental Disruption Recovery), `CC7.2-CC7.4` (Tamper-Evident Audit Evidence) |
| **ISO/IEC 27001:2022** | `iso27001` | `A.8.13-A.8.14` (Information Backup & Redundancy), `A.8.17` (Clock Synchronization Resilience), `A.8.29` (Security & Resilience Testing in Acceptance) |
| **Bank Indonesia / OJK** (SEOJK 29/03/2022 & PBI) | `bi-ojk` | `SEOJK-29/MRTIK-DRC` (Uji Ketahanan & Pemulihan Bencana), `SEOJK-29/Integritas-Data` (Konsistensi Data Transaksi), `SEOJK-29/Jejak-Audit` (Bukti Audit Kriptografis) |

Each control is evaluated strictly from empirical evidence (`PASS`, `PARTIAL` when fault coverage or cryptographic signing is incomplete, or `FAIL`). Every dossier includes an explicit **Methodology & Scope Boundaries** disclosure detailing what was tested, which fault classes were omitted, and the difference between deterministic simulation and live cluster execution.

---

## Empirical Validation

### Simulated Bug Corpus (`pkg/sim/raft`)

Detection rates measured over 100–200 seeds on a 5-node cluster (10 simulated seconds per seed):

| System | Fault Mechanism Under Test | Required Faults | Observed Violation Rate |
| :--- | :--- | :--- | :--- |
| `raft` | Correct Raft (reads committed through log) | all 8 fault kinds | **0 / 800 runs** |
| `raft-stale-reads` | Deposed leader serves local reads without quorum | `partition` | **~36% of seeds** |
| `raft-lease` | Leader lease timed with local wall clock | `clock-skew` + `partition` | **~5% of seeds** (0% without `clock-skew`) |
| `raft-unsafe-fsync` | Votes & log entries acked before `fsync` | `kill` + `io-stall` | **~3.5% of seeds** (0% under `partition` alone) |

### Live Cluster CI Validation (`.github/workflows/live.yml`)

Every push runs live chaos tests against real containerized clusters:

| Cluster | Faults Injected | Throughput | Outcome |
| :--- | :--- | :--- | :--- |
| **etcd 3.5** (3 nodes, [`examples/etcd`](examples/etcd)) | `partition`, `kill`, `pause` | ~16,000 ops / 60s | `LINEARIZABLE` |
| **Valkey 8** (1 primary + 2 async replicas, [`examples/valkey`](examples/valkey)) | `partition`, `pause` | ~19,000 ops / 60s | `VIOLATION` (stale reads from partitioned replica, asserted via `--expect violation`) |
| **Valkey 8** (single primary with `appendfsync always`) | `kill -9` | ~10,000 ops / 45s | `LINEARIZABLE` (acknowledged writes survive crash) |

Additional Docker Compose environments are provided in [`examples/postgres`](examples/postgres), [`examples/kafka`](examples/kafka), and [`examples/tidb`](examples/tidb).

---

## Architecture

```mermaid
flowchart LR
  seed(("64-bit Seed")) --> sched["nemesis.Generate<br/>Fault Schedule"]
  seed --> wl["pkg/workload<br/>PRNG / Wasm 1.0 VM"]
  sched --> simw["pkg/sim<br/>Discrete-Event Simulator"]
  wl --> simw
  sched --> live["pkg/runner + strife-agent<br/>RTNETLINK · cgroup v2 · Signals"]
  wl --> live
  simw --> hist[("history.jsonl")]
  live --> hist
  live --> probes[("probes.jsonl")]
  hist --> chk["pkg/checker + pkg/txn<br/>WGL + Elle DSG Cycles"]
  probes --> corr["pkg/probe<br/>Root-Cause Correlator"]
  chk --> corr
  corr --> rep["pkg/report<br/>HTML5 Studio · JUnit · JSON"]
  rep --> att["pkg/attest + pkg/compliance<br/>Ed25519 in-toto · Audit Dossier"]
```

```text
cmd/strife        CLI: sim, campaign, shrink, check, plan, run, workload, keygen, sign, verify, compliance, systems
cmd/strife-agent  Per-node daemon: Linux AF_NETLINK netem, direct signals, cgroup v2, /probes telemetry
internal/rng      Pinned, forkable xoshiro256** PRNG seeded with SplitMix64
pkg/history       Jepsen-style operation histories, JSONL codec, SHA-256 hashing
pkg/model         Sequential specifications: kv, set, queue, counter, idgen, txn
pkg/txn           Direct Serialization Graph (DSG) & Adya anomaly cycle detector (G0, G1a/b/c, G2, G-realtime)
pkg/checker       Wing & Gong / Lowe linearizability solver, P-compositionality, witness diagnosis
pkg/nemesis       Deterministic fault schedule generator & command lowering
pkg/workload      Declarative workload compiler, presets, and fuel-metered WebAssembly 1.0 stack machine
pkg/sim           Discrete-event simulation world (network, timers, disk flushes, local clocks)
pkg/sim/raft      Reference Raft consensus implementation with configurable safety bugs
pkg/explore       Parallel seed exploration & bit-for-bit determinism verification
pkg/campaign      Multi-seed campaign runner, baseline regression tracker, HTML heatmap dashboard
pkg/shrink        1-minimal scenario & history delta debugging (with isSoundHistory invariant)
pkg/transport     Execution transports: docker exec, ssh, strife-agent HTTP/Unix socket, dry-run
pkg/agent         Agent HTTP server, Linux RTNETLINK encoder, cgroup v2 controller, jitter tracker
pkg/probe         Server-side telemetry collector (fsync canary, Linux PSI, OOM, clock jumps) & correlator
pkg/client        Zero-dependency clients: etcd v3, Redis/Valkey, PostgreSQL, Kafka, MySQL/TiDB
pkg/runner        Live cluster test orchestrator & per-run namespace isolation
pkg/report        Interactive HTML5/Canvas timeline studio, Markdown/JUnit exporters, fingerprints
pkg/attest        in-toto v0.1 statement builder and Ed25519 signing/verification
pkg/compliance    Regulatory control evaluator (EU DORA, SOC 2, ISO 27001, BI/OJK) & HTML/MD/JSON dossiers
site/             Interactive product showcase & standalone HTML5 demos (timeline, campaign, dossier)
```

<details>
<summary><strong>CLI Reference & Exit Codes</strong></summary>

<br>

| Command | Description |
| :--- | :--- |
| `strife sim [flags]` | Run a single seed or sweep `--seeds 1..N` in the deterministic simulator (`--determinism`, `--shrink`, `--workload`) |
| `strife campaign [flags]` | Run a multi-seed campaign, emit `campaign.html`, and diff against `--baseline` (`--expect no-regression`) |
| `strife shrink [flags]` | Minimize a failing simulation (`--seed` / `--report`) or recorded trace (`--history FILE`) to 1-minimality |
| `strife check [flags]` | Verify a recorded `history.jsonl` against `--model` (`kv`, `set`, `queue`, `counter`, `idgen`, `txn`) and optional `--probes` |
| `strife plan [flags]` | Print the seeded fault schedule and exact host/container commands without executing them |
| `strife run [flags]` | Execute a live chaos test defined by `strife.json` (`--dry-run`, `--faults`, `--expect`) |
| `strife workload [flags]` | List presets (`--list`), compile specs to `.wasm` (`--compile --out`), or inspect `.wasm` plugins (`--inspect`) |
| `strife keygen [flags]` | Generate an Ed25519 signing keypair (`<prefix>.key` and `<prefix>.pub`) |
| `strife sign [flags]` | Sign a `report.json` into an `in-toto` v0.1 attestation bundle (`bundle.json`) |
| `strife verify [flags]` | Verify a signed `bundle.json` against an Ed25519 public key and optional `history.jsonl` |
| `strife compliance [flags]` | Evaluate a report/campaign and attestation bundle against `dora,soc2,iso27001,bi-ojk` |
| `strife systems` | List built-in simulated systems and the fault combinations that expose their bugs |

**Exit Codes:**
- `0` — `linearizable` (or matches `--expect` / all compliance controls pass)
- `1` — `violation` (or regression / compliance failure / signature mismatch)
- `2` — invalid CLI usage or configuration error
- `3` — `unknown` (checker search budget or timeout reached without a conclusive verdict)
- `4` — runtime or transport execution error

</details>

---

## CI Integration

Use the composite GitHub Action (see [`docs/github-action.md`](docs/github-action.md) for inputs, outputs, and GitLab/Jenkins recipes):

```yaml
- uses: kemalhafizh/strife@main
  with:
    command: sim --system raft --seeds 1..500 --determinism --expect linearizable
```

## Development

```sh
make test        # Unit tests + bug-finding regression suite
make test-short  # Fast unit tests with the race detector (-race)
make chaos       # Full CI gauntlet: 200 seeds of Raft + bug discovery + shrinking verification
```

See [`docs/ROADMAP.md`](docs/ROADMAP.md) for architectural decisions, benchmark methodology, and roadmap history.

## License

Apache License 2.0. See [`LICENSE`](LICENSE).
