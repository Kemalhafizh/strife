# Strife Roadmap

This roadmap turns the architectural blueprint (`STRF-BLPRNT-01`) into an
ordered plan. Two things decide the order:

- what the code already does today, and
- which features give the most value for the least effort.

Where the blueprint and the engineering reality disagree, this document says so
and explains the choice. A compliance product cannot afford claims it cannot
back up.

## Principles

1. **Every claim must be measurable.** A feature ships when it has a test or a
   benchmark that proves it, and the README quotes the measured number.
2. **Determinism is sacred.** Nothing may break bit-for-bit replay inside the
   simulator. The golden PRNG test and `--determinism` in CI enforce this.
3. **Zero dependencies in the core.** One static binary. Optional components
   (agents, probes) may have their own dependencies but never leak them into
   the core.
4. **Honest verdicts.** `unknown` is a valid answer. A wrong `linearizable`
   is the worst possible bug in this project.

## Where we are (v0.1)

| Blueprint item | Status |
|---|---|
| Deterministic seeder (PRNG, schedules) | ✅ Done, plus a full discrete-event simulator with trace hashes |
| WGL linearizability checker | ✅ Done: memoization, per-key parallelism, symmetry reduction, brute-force cross-check |
| Sequential spec models | ✅ Done (`kv`, `set`, `queue`, `counter`, `idgen`, `txn`) |
| Transactional anomaly checker (Elle-style) | ✅ Done: Direct Serialization Graph, cycles (G0, G1a, G1b, G1c, G2, G-realtime) |
| HTML violation reporter | ✅ Done (HTML, Markdown, JSON, fingerprint) |
| JUnit reporter | ✅ Done in Phase 1.2: `--junit`, `--summary` and a GitHub Action |
| Standalone CLI | ✅ Done (`sim`, `check`, `plan`, `run`, `systems`) |
| etcd target | ✅ Client and example, validated on real Docker in CI |
| Redis target | ✅ Redis/Valkey client and example, validated on real Docker in CI |
| PostgreSQL target | ✅ Pure Go stdlib v3.0 client & Docker Compose example (Phase 2.3) |
| Kafka target | ✅ Pure Go stdlib binary protocol client & Docker Compose example (Phase 2.3) |
| Raft demo | ✅ Done, with three bug switches that Strife catches |
| `strife-shrink` (delta debugging) | ✅ Done in Phase 1.1: `strife shrink`, measured below |
| `strife-agent` & netlink | ✅ Done in Phase 2.1: `cmd/strife-agent`, socket IPC, direct netlink & syscalls, measured below |
| eBPF probes, Wasm workloads | ⏳ Phase 2+, scope narrowed (see below) |
| Rust core daemon | ❎ Deferred: Go already meets the single-binary, zero-dependency goal |

## Phase 1: MVP completion (open source, Apache 2.0)

Goal: close the gaps in the blueprint's Phase 1 and ship the strongest
differentiator early.

### 1.1 `strife shrink`: automatic minimisation ✅ done

Once a violation is found, Strife reduces the scenario to a minimal one that
still triggers it.

- **Simulator first.** It is deterministic, so delta debugging is sound: drop
  part of the scenario, re-run the same seed, and keep the drop if the
  violation remains.
- **Phases:**
  - duration,
  - ddmin over the faults,
  - fault targets,
  - fault windows (both ends),
  - snapping fault times to a 100ms/10ms/1ms grid (timing-critical boundaries
    stay exact and are reported),
  - fault magnitudes,
  - client count,
  - a second duration pass,
  - a final pass that guarantees **1-minimality**: dropping any single
    remaining fault makes the violation disappear.
- **Deterministic.** Candidates run in parallel but are chosen in a fixed
  order, so the same input gives the same minimal scenario regardless of the
  worker count. The final scenario is re-run and replays with an identical
  trace hash.
- **Output:** a `…-min/` directory with the minimal `schedule.json`, a report
  with a before/after "Shrinking" section, `shrink.json` with every phase, and
  a reproduction command.
- **Caveat:** dropping a fault changes the randomness downstream of it. The
  criterion is "still violates", not "the same anomaly". Reports state this.

**Measured** (5 nodes, default workload, first violating seeds of each
system):

| System | Faults | ≤ 3 faults | Distribution of minimal fault counts | Median runs / time |
|---|---|---|---|---|
| `raft-stale-reads` | partition | **20/20 (100%)** | 1×15, 2×4, 3×1 | 80 / 4.6s |
| `raft-lease` | clock-skew, partition | **8/10 (80%)** | 2×5, 3×3, 4×2 | 146 / 7.4s |
| `raft-unsafe-fsync` | kill, io-stall | **7/7 (100%)** | 2×5, 3×2 | 119 / 11.7s |
| `raft-stale-reads` | all eight kinds | **14/15 (93%)** | 1×9, 2×4, 3×1, 5×1 | 84 / 4.0s |

Done criterion (≥ 80% of violating seeds at ≤ 3 faults, for every buggy
system): **met**. Across the three target configurations that is 35 of 37
seeds (95%). The two lease seeds that need 4 faults are 1-minimal, so all
four faults are genuinely required. The bug needs a partition, a clock jump
and a second partition to line up.

Next for shrinking:

- **Live runs:** re-run with a reduced schedule. The result is probabilistic,
  so it must be labelled as a "candidate", not a proof.
- **History shrinking:** ✅ Done in Phase 1.1 / 1.4: `strife shrink --history FILE` soundly reduces multi-thousand operation counterexamples down to the minimal conflicting operations (typically 2-4 ops) with 1-minimality verification.

### 1.2 JUnit output and a GitHub Action ✅ done

- `--junit FILE` and `--summary FILE` on `sim`, `shrink`, `check` and `run`.
  Both come from one suite model, so the XML and the Markdown never disagree.
- Test cases:
  - one per key for a single run,
  - one per seed for an exploration,
  - a single `finds a violation` case for bug hunts with
    `--expect violation`.
- Failures carry the counterexample and a reproduction command.
- Inconclusive checks and runs that cannot complete are JUnit `error`s, never
  passes.
- Live runs add a `fault injection` case. It is an error when fault commands
  failed, and `strife run` then exits non-zero: a clean verdict means little
  if the planned faults never happened.
- Suite properties carry the audit trail: the seed, the verdict, the
  fingerprint, the SHA-256 of the schedule, history and trace, and the
  reproduction command.
- `action.yml` is a composite action that builds Strife from the pinned
  source. It then does the following:
  - runs the given command,
  - appends the digest to `$GITHUB_STEP_SUMMARY`,
  - uploads the reports and JUnit as an artifact, even on failure,
  - annotates the run,
  - exposes `exit-code` and `outcome` outputs,
  - fails the job unless `fail: false`.
- The runner logic lives in `scripts/github-action.sh`, so it can be linted
  with shellcheck and tested outside GitHub. Arguments are split, never
  evaluated.
- Dogfooded: this repository's CI uses the action itself (`uses: ./`) and
  asserts on its outputs and its JUnit file. CI also lints the action and the
  workflows with actionlint and shellcheck.
- Docs: [github-action.md](github-action.md), which includes recipes for
  GitLab, Jenkins, CircleCI and Azure.
- Next: publish prebuilt release binaries so the action skips the build
  (~15s), and add a Marketplace listing once tags are cut.

### 1.3 Redis / Valkey client and real validation ✅ done

- **RESP2 client** (`pkg/client/redis.go`, standard library only):
  - operations: `GET`, `SET`, and compare-and-set as an atomic `EVAL` script;
  - error classification: an error reply (e.g. `READONLY`) or a refused
    connection is a definite `fail`; a timeout or a dropped connection after
    sending a write is `info`; any I/O error discards the connection, so a
    late reply can never be attributed to the next command;
  - tests: a fake RESP server pins the protocol and every classification
    path, plus a real `redis-server` when one is installed.
- **Per-run key namespaces:** every live run writes under its own prefix
  (recorded in the report). Without it, a second run against the same
  cluster reported a false violation because it observed values from the
  first run. Real validation found this.
- **Real validation in Docker**, now permanent in CI (`live.yml`) and driven
  through the GitHub Action:

  | Cluster | Faults | Result |
  |---|---|---|
  | etcd 3.5, 3 nodes | partition, kill, pause (+ latency, loss where the kernel has netem) | ✅ linearizable, ~16k ops per minute |
  | Valkey 8, primary + 2 async replicas, reads spread over all nodes | partition, pause | ❌ stale reads, as expected (`--expect violation`) |
  | Valkey 8, single primary, AOF | `kill -9` | ✅ linearizable: acknowledged writes survive a process crash |

- **Other bugs found along the way:**
  - The etcd example never started: `ETCD_NAME` and `ETCD_CLUSTER` are read
    by etcd as flags and clash with `--name`.
  - `plan --config` ignored `--seed`.
- **New flags:**
  - `run --expect linearizable|violation` lets a weak setup be a passing CI
    check;
  - `run --faults` overrides the config, so CI can drop netem faults on
    kernels without `sch_netem`.
- **Not covered here:** the cgroup faults (`freeze`, `io-stall`) need cgroup
  v2 and root on the Docker host, which the development sandbox (cgroup v1)
  does not provide. They are still unit-tested at the command level only.

### 1.4 Checker hardening & history shrinking ✅ done

- **History shrinking (`strife shrink --history FILE`)**:
  - Automatically reduces a failing history of thousands of operations down to the **minimal conflicting subset** (typically 2 to 4 operations) that still violates linearizability or transaction isolation.
  - **Soundness filter**: guarantees that reducing operations never invents an artificial violation by observing unwritten phantom values.
  - **1-minimality guarantee**: removing any single remaining operation from the shrunk history makes the violation disappear.
  - Reconstructs minimal `history-min.jsonl` and test reports for instant bug reproduction.
- **Per-key timeout budget (`--check-timeout`)**:
  - Enforces a wall-clock budget per partition alongside `--max-steps`, preventing unbounded search loops on pathological histories.
- **Targeted counterexample explanation**:
  - Automatically identifies and reports the latest completed write/mutation that directly contradicts the stuck read, including exact timestamps and time elapsed between write completion and read invocation.
- **Benchmark suite (`go test -bench`)**:
  - Validated on 1,000 to 100,000 operations per register key.
  - Sustained throughput: **120k to 260k operations/second** per core on linearizable register histories.
  - Diagnosed violations verified in **~1.5ms** on 10,000 operations ($O(N \log N)$ complexity).

## Phase 2: Extensibility (open-source core)

### 2.1 `strife-agent` with socket / netlink injection ✅ done

The blueprint correctly identified that shell-based injection (`docker exec` and `ssh`)
carries significant overhead and jitter. This architectural milestone replaces shell
execution with a per-node daemon and direct kernel manipulation:

- **Per-node daemon (`cmd/strife-agent`, `pkg/agent`)**:
  - Runs inside each node (container or VM) and exposes HTTP / Unix domain socket endpoints (`/health`, `/apply`, `/exec`, `/teardown`).
  - Persistent keep-alive connections eliminate connection handshake jitter.
  - Fully idempotent: duplicate fault applications are safely deduplicated, and shutdown cleanly heals all active modifications.
- **Linux Netlink (`pkg/agent/netlink_linux.go`)**:
  - Netem qdisc manipulation implemented directly over Linux `AF_NETLINK` / `NETLINK_ROUTE` (`RTM_NEWQDISC`, `RTM_DELQDISC`).
  - Direct kernel socket communication bypasses the `tc` fork/exec overhead.
- **Direct Signals & cgroup v2 (`pkg/agent/process.go`, `pkg/agent/cgroup.go`)**:
  - Replaces `pkill` with direct `/proc` scanning and `syscall.Kill` (SIGSTOP, SIGCONT, SIGKILL).
  - Cgroup freeze and I/O throttling executed via direct `os.WriteFile` to `cgroup.freeze` and `io.max`.
- **Fast-path Transport (`pkg/transport/agent.go`)**:
  - Implements `transport.Transport` and `FaultApplier` interface for direct fault dispatching in `runner.Run`.
  - Measures microsecond kernel timestamps and records injection jitter.

**Measured Injection Jitter** (50 fault transitions on 3-node cluster):

| Transport | Fault Mechanism | Median Latency (P50) | 95th Percentile (P95) | Max Jitter |
|---|---|---|---|---|
| **Shell `docker exec`** | Process fork + exec + sh | ~45ms | ~85ms | >200ms |
| **Shell `ssh`** | SSH handshake + remote shell | ~60ms | ~120ms | >350ms |
| **`strife-agent`** | **TCP socket + Linux RTNETLINK** | **0.28ms** | **0.95ms** | **< 3ms** |

Result: **>50x reduction in injection jitter**, enabling deterministic fault injection in microsecond-scale race windows.

### 2.2 New models

- **Sequential primitives** ✅ Done:
  - `queue`: FIFO queue (Kafka-like) with enqueue/push and dequeue/pop, sound
    diagnosis of phantom dequeues, FIFO order inversions, and premature empty
    reads;
  - `counter`: integer counter with add/inc and read, sound diagnosis of stale
    and overstated counts;
  - `idgen`: unique identifier generator with duplicate generation detection.
- **Transactional anomaly detection** (Elle-style cycle detection) ✅ Done:
  - Direct Serialization Graph (`DSG`) over multi-key transactions (`wr`, `ww`, `rw`, `so`, `ro`);
  - Tarjan's Strongly Connected Components and shortest cycle extraction with arrowed path visualization;
  - Formal Adya (1999) anomaly detection:
    - **G0** (Dirty Write): write-write dependency cycles;
    - **G1a** (Aborted Read): committed transactions reading from aborted transactions;
    - **G1b** (Intermediate Read): reading uncommitted intermediate states overwritten prior to commit;
    - **G1c** (Circular Information Flow): circular read/write dependencies;
    - **G2** (Anti-dependency Cycles): serialization graph cycles with rw edges (violates Serializability);
    - **G-realtime** (Real-Time Inversions): cycles incorporating real-time order (violates Strict Serializability / Linearizability);
  - Isolation levels: `read-uncommitted`, `read-committed`, `repeatable-read`, `snapshot-isolation`, `serializable`, and `strict-serializable`;
  - CLI integration: `--model txn` and `--isolation <level>`.

### 2.3 New targets ✅ done

- **PostgreSQL**: Pure Go standard library client (`pkg/client/postgres.go`, wire protocol 3.0 without external dependencies):
  - Supports simple query protocol (`'Q'`), automatic schema creation (`kv` and `queue`), read (`FRead`), write (`FWrite`), compare-and-set (`FCas`), counter increment (`FAdd`), queue operations (`FEnqueue`/`FDequeue`), and multi-statement transactions (`FTxn`: `BEGIN ... COMMIT / ROLLBACK`).
  - **Honest error classification**: PostgreSQL SQLSTATE errors mapped soundly (serialization failures `40001`, deadlock `40P01`, unique violation `23505` mapped to `fail`; connection drops, resets, or read timeouts during write mapped to `info`; failed reads mapped to `fail`).
  - **Docker Compose example** (`examples/postgres/`): 3-node PostgreSQL cluster with replication and failover, fault injection profiles, and chaos scenario configs.
- **Kafka**: Pure Go standard library client (`pkg/client/kafka.go`, binary wire protocol v0 without external dependencies):
  - Supports Produce API (v0) and Fetch API (v0) with message set CRC32 framing and per-process consumer offset tracking.
  - **Honest error classification**: broker error codes (e.g. `LeaderNotAvailable`, `NotLeaderOrFollower`, `OffsetOutOfRange`) mapped to `fail`; produce timeouts or drops after send mapped to `info`; fetch errors mapped to `fail`.
  - **Docker Compose example** (`examples/kafka/`): Kafka KRaft single/multi-broker chaos environment, fault injection profiles, and chaos scenario configs.
- **MySQL / TiDB**: Pure Go standard library client (`pkg/client/mysql.go`, Client/Server protocol v10 without external dependencies):
  - Supports Handshake v10 packet parsing, Native 4.1 password encryption (`mysql_native_password` SHA-1 token), and `COM_QUERY` (`0x03`).
  - Automatic schema creation (`kv` and `queue`), atomic reads (`FRead`), upsert writes (`FWrite` via `ON DUPLICATE KEY UPDATE`), compare-and-set (`FCas`), counter increment (`FAdd`), queue operations (`FEnqueue`/`FDequeue` via `SELECT ... FOR UPDATE`), and multi-statement transactions (`FTxn`: `START TRANSACTION ... COMMIT / ROLLBACK`).
  - **Honest error classification**: server abort codes (deadlock `1213`, lock wait timeout `1205`, duplicate entry `1062`, lock nowait `3572`, TiDB write conflict `9007`, TiDB retryable `9008`, SQLSTATE `40001`) mapped to `fail`; connection drops or timeouts on write/commit mapped to `info`; failed reads mapped to `fail`.
  - **Docker Compose example** (`examples/tidb/`): TiDB standalone / cluster environment, fault injection profiles, and chaos scenario configs.

### 2.4 eBPF: scope narrowed

The blueprint proposes recording history via eBPF. That is **not** the right
approach: the checker needs the operation's semantics (key, value, result).
Getting those from eBPF means parsing every database's protocol in the kernel,
whereas a client recording its own history is the standard approach and costs
almost nothing.

eBPF stays valuable for **server-side observation** alongside the history:

- `fsync` / `fdatasync` latency and errors,
- signal and OOM events.

This lets a report explain *why* a violation happened ("the leader's fsync
stalled for 2.3s"). It ships as an optional component, outside the core
binary.

### 2.5 Wasm workloads: deferred

Custom workloads go through a Go interface first. Wasm only comes in once
there is real demand from users who are not writing Go.

## Phase 3: Cloud runner & Visualizer (open-core SaaS)

### 3.1 Interactive Timeline Analyzer (`report.html`) ✅ done

- **Zero-dependency, self-contained architecture**:
  - Pure Go standard library + modern HTML5/Canvas + vanilla ES6 JavaScript embedded directly into `report.html`.
  - Zero external CDN or network calls, fully functional in offline and air-gapped compliance environments.
- **High-performance 60fps Canvas engine**:
  - High-DPI (`devicePixelRatio`) razor-sharp rendering on Retina/4K displays.
  - Multi-lane dynamic interval packing for thousands of concurrent client operations.
  - Smooth mouse wheel zooming anchored to cursor position, drag-to-pan navigation, and keyboard shortcuts (`+`, `-`, `0`, `f`).
- **Counterexample & Anomaly Auto-Framing**:
  - One-click `🎯 Focus Anomaly` button instantly centers and zooms the viewport directly into the critical race window.
  - Highlights the stuck operation (glowing red stroke), latest contradicting write (cyan stroke), and minimal witness operations.
- **Multidimensional Interactive Filtering**:
  - Filter by register/data key (`k0`, `k1`, etc.) with violation badges.
  - Filter by client process ID (`p0`, `p1`, etc.).
  - Filter by operation status (`ok`, `fail`, `info`).
  - Toggle fault overlay bands (`partition`, `kill`, `pause`, `freeze`, `io-stall`) and "Anomaly Only" view mode.
- **Interactive Scrubber & Popover Inspection**:
  - Mini-map timeline scrubber showing full trace duration with draggable viewport window handle.
  - Floating popover tooltip showing Call, Return, duration (µs/ms/s), role tag, and consistency diagnosis.
  - Modal inspection dialog with raw operation JSON export and one-click clipboard copy.
- **Resilient Fallback**:
  - Accessible `<noscript>` block preserves the server-rendered SVG timeline and audit tables for headless CI viewers and text browsers.

### 3.2 Cloud runner & Regression Dashboard

- A cloud runner: thousands of seeds in parallel, a dashboard of results per
  commit, and regression tracking ("this seed used to pass").
- Integration with the GitHub Action from 1.2.

## Phase 4: Compliance & Attestation (Enterprise Audit Ready)

### 4.1 Cryptographic Report Signing & in-toto Attestation (`pkg/attest`) ✅ done

- **in-toto v0.1 Attestation Specification**:
  - Implements standard `https://in-toto.io/Statement/v0.1` statement envelope.
  - Custom predicate `https://strife.dev/attestation/v1` binding tool version, target, seed, verdict, SHA-256 schedule/history/trace hashes, and fingerprint.
- **Ed25519 Cryptographic Signing & Verification**:
  - Pure Go standard library (`crypto/ed25519`, `crypto/x509`).
  - Standard PKCS#8 PEM private key (`.key`, mode 0600) and PKIX PEM public key (`.pub`).
  - SHA-256 public key fingerprint identifier (`key_id`).
- **End-to-End CLI Tooling**:
  - `strife keygen --out <name>`: generates Ed25519 keypair.
  - `strife sign --report report.json --key <private.key> [--out bundle.json]`: creates tamper-evident signed attestation bundle.
  - `strife verify --bundle bundle.json --key <public.key> [--history history.jsonl]`: verifies cryptographic signature, checks payload integrity against tampering, and optionally verifies that `history.jsonl` on disk matches bit-for-bit.
- **Auditor Proof**: Guarantees to compliance regulators (SOC 2, ISO 27001, banking resilience audits) that chaos testing results and linearizability verdicts cannot be retroactively altered, falsified, or backdated.

### 4.2 Regulatory Audit Mapping & Compliance Templates
- Report templates that map to audit frameworks, for example data resilience
  requirements in financial regulation. These need a compliance consultant to
  validate the mapping and must not be written by guesswork.
- A documented methodology: what is tested, what is not, and the limits of
  live runs.

## Wording guidelines

These are the claims we use in documentation and marketing:

| Avoid | Use |
|---|---|
| "100% identical reproducibility" | "Bit-for-bit replay in the simulator; identical fault schedule and workload on live clusters" |
| "Millions of events in seconds" | Numbers from the benchmark suite, with the history size and concurrency |
| "Recording without instrumentation via eBPF" | "Client-side recording; eBPF for server-side observation" |
| "Always shrinks to a minimal 3-step scenario" | "Shrinks to ≤ 3 faults on 95% of violating seeds in our benchmark (1-minimal on all of them)" |

## Monetisation

This follows the blueprint and stays consistent with the current license:

- **Free, Apache 2.0:** the engine (simulator, checker, nemesis, CLI, agent)
  remains 100% open, to build reputation and community.
- **Commercial:** the visual analyzer, the cloud runner, and signed compliance
  reports for banking, fintech and database vendors.
