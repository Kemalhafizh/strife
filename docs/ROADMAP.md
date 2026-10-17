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
| Server-side probes & Wasm workloads | ✅ Done in Phase 2.4 (`pkg/probe`) & Phase 2.5 (`pkg/workload`, pure-Go Wasm 1.0 VM) |
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

> ⚠️ **Proof debt:** no benchmark or CI job in this repository produces the
> numbers below yet, so they cannot be reproduced. Until 5.0 lands they are
> targets, not measurements, and must not be quoted externally.

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

### 2.4 Server-Side Diagnostic Probe & Root-Cause Correlator (`pkg/probe`) ✅ done

The blueprint originally proposed recording history via eBPF. That is **not**
the right approach for the history itself: the checker needs the operation's
semantics (key, value, result), which clients record directly at near-zero cost.

Instead, server-side telemetry is implemented for **root-cause observation and
correlation** alongside the client history (`pkg/probe`):

- **Server-Side Telemetry Collector (`pkg/probe/collector.go`)**:
  - Polls Linux Pressure Stall Information (`/proc/pressure/io`, `/proc/pressure/cpu` `some`/`full` `total=` microsecond deltas),
  - Monitors cgroup v2 `memory.events` for `oom_kill` increments,
  - Measures active `fsync` canary latency and errors (`EIO`/`ENOSPC`) on the target storage volume,
  - Detects non-monotonic wall-clock jumps (`clock_jump`),
  - Captures process signal delivery (`SIGKILL`, `SIGSTOP`, `SIGCONT`, `CGROUP_FREEZE`) inside `strife-agent` (`GET /probes`).
- **Root-Cause Correlator (`pkg/probe/correlate.go`)**:
  - Cross-references linearizability counterexample windows (and indeterminate `info` writes) against server-side probe events (`probes.jsonl`) and active nemesis faults,
  - Produces ranked root-cause attributions (`high` / `medium` / `low` confidence) explaining *why* a violation or indeterminate write occurred (e.g., *"the node's fsync stalled for 95ms during the violating read window"*),
  - Integrated into `strife check --probes probes.jsonl`, `strife run` (via `strife-agent`), and rendered in `report.md` and `report.json`.

### 2.5 Custom Workload Plugins & Wasm Runner (`pkg/workload` & `strife workload`) ✅ done

- **Zero-Dependency WebAssembly 1.0 (MVP) Stack Machine (`pkg/workload/wasm.go`)**:
  - Pure Go standard library WebAssembly 1.0 binary (`\x00asm\x01\x00\x00\x00`) parser, validator, and bytecode interpreter with precomputed structured control-flow jump tables (`block`, `loop`, `if`, `else`, `br`, `br_if`, `call`, `select`, 32/64-bit integer arithmetic/comparisons, globals, and linear memory).
  - **Fuel-Metered Sandbox**: Enforces a per-invocation instruction budget (`DefaultMaxFuel = 10,000`) and call-depth cap (`32`), guaranteeing that an untrusted or buggy `.wasm` workload with an infinite loop traps cleanly without hanging the simulator or live runner.
  - **100% Deterministic ABI**: Invokes exported `next_op(process: i32, step: i32, rand_u64: i64, keys: i32, values: i32) -> i64` fed exclusively from each client's forked `xoshiro256**` PRNG stream, preserving bit-for-bit replay (`--determinism`) and SHA-256 module digest tracking.
- **Declarative Workload Spec & Wasm Compiler (`pkg/workload/workload.go`)**:
  - Compiles declarative JSON workload definitions (`CompileSpecToWasm`) and 6 built-in enterprise presets (`preset:kv-balanced`, `preset:read-heavy`, `preset:write-heavy`, `preset:cas-hotspot`, `preset:sequential-scan`, `preset:si-write-skew`) directly into standalone `.wasm` binaries.
  - Supports `uniform`, `hotspot` (e.g., 80% traffic concentrated on 20% hot keys), `sequential`, and `partitioned` key distributions.
- **CLI & Runner Integration (`strife workload`, `strife sim --workload`, `strife campaign --workload`, `strife run`)**:
  - `strife workload --list`, `strife workload --compile <spec.json|preset:name> --out <plugin.wasm>`, and `strife workload --inspect <plugin.wasm>`.

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

### 3.2 Campaign Runner & Regression Dashboard (`pkg/campaign` & `strife campaign`) ✅ done

- **Parallel Multi-Seed Campaign Engine (`pkg/campaign/runner.go`)**:
  - Executes hundreds or thousands of deterministic seeds in parallel across worker goroutines with deterministic ordering and aggregate SHA-256 campaign fingerprinting.
  - Automatically generates detailed per-seed timeline reports (`--keep`) and optionally shrinks violating seeds (`--shrink`) within the campaign run.
- **Commit-to-Commit Regression & Drift Tracker (`pkg/campaign/diff.go`)**:
  - Compares current seed outcomes against a baseline manifest (`--baseline` / `--save-baseline`) to classify transitions into `regressed` (`linearizable → violation`), `fixed` (`violation → linearizable`), `persistent_failure`, `new_failure`, and `trace_drift` (identical verdict with altered execution trace hash).
  - Priority corpus replay (`--replay-failing`): automatically merges previously failing seeds from the baseline into subsequent campaign runs so regressions cannot slip past a shifted seed window.
- **Self-Contained Campaign & Regression Dashboard (`campaign.html`)**:
  - Interactive dark-theme HTML5 dashboard with KPI cards, clickable per-seed heatmap tiles, status/delta filter pills (`All`, `Violations`, `Regressed`, `Fixed`, `Trace Drift`, `Linearizable`), live search, and one-line reproduction commands.
- **CI & GitHub Action Integration (`pkg/campaign/suite.go` & `scripts/github-action.sh`)**:
  - Supports `--expect no-regression`, `--junit`, and `--summary` (`$GITHUB_STEP_SUMMARY`), surfacing seed regressions directly in pull request checks and step summaries.

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

### 4.2 Regulatory Audit Mapping & Compliance Dossier (`pkg/compliance` & `strife compliance`) ✅ done

- **Empirical Technical Control Mapping (`pkg/compliance/compliance.go`)**:
  - Maps provable technical resilience and consistency properties (network partition survival, crash/`fsync` durability, clock-skew tolerance, linearizability/isolation verification, and Ed25519 `in-toto` non-repudiation) to four regulatory and audit standards:
    - **EU DORA** (`dora`): `DORA-Art.24-25` (Operational Resilience Testing), `DORA-Art.9.4(c)` (Data Integrity Under Disruption), `DORA-Art.11-12` (Crash Recovery & Storage Durability).
    - **AICPA SOC 2** (`soc2`): `PI1.3-PI1.4` (Processing Integrity), `A1.2` (Environmental Disruption Recovery), `CC7.2-CC7.4` (Tamper-Evident Audit Evidence).
    - **ISO/IEC 27001:2022** (`iso27001`): `A.8.13-A.8.14` (Information Backup & Redundancy), `A.8.17` (Clock Synchronization Resilience), `A.8.29` (Resilience Testing in Development & Acceptance).
    - **Bank Indonesia / OJK** (`bi-ojk`): `SEOJK-29/MRTIK-DRC` (Uji Ketahanan & Pemulihan Bencana), `SEOJK-29/Integritas-Data` (Integritas & Konsistensi Data Transaksi), `SEOJK-29/Jejak-Audit` (Bukti Audit Kriptografis).
  - Evaluates each control as `PASS`, `PARTIAL` (with explicit remediation hints when fault coverage or cryptographic signing is incomplete), or `FAIL`.
- **Documented Methodology & Scope Boundaries**:
  - Explicitly states *What Is Tested*, *What Is Not Tested* (omitted fault classes, Byzantine/hardware bit-rot, organizational policies), and *Execution & Reproducibility Boundaries* (simulation bit-for-bit replay vs. live cluster interleavings), plus a clear auditor disclaimer.
- **Audit Dossier Artifacts & CLI (`strife compliance`)**:
  - Generates tamper-evident `dossier.json`, `dossier.md`, and a self-contained, print-ready `dossier.html` from either a single `report.json` or a multi-seed `campaign.json` (plus optional `bundle.json` + Ed25519 public key).

## Phase 5: Proof, reach, distribution (next)

An audit of `main` at `22a3dff` (2026-10-17): the build, `go vet`, `gofmt`
and the full test suite are clean, and the `ci`, `live` and `pages` workflows
are green. Feature coverage now runs ahead of evidence. The live CI
validates only etcd and Valkey. The new clients are tested against fake
servers only. One headline number on the README and the site has no
benchmark behind it. Principle 1 says a feature ships when a test or
benchmark proves it, so Phase 5 pays that debt first and only then widens
reach.

Order: **5.0 → 5.1 → 5.2** are prerequisites for the first paying customer.
5.3 and 5.4 make the engine stronger. Phase 6 is the commercial layer.

### 5.0 Pay the proof debt ⬜ (highest priority, ~1–2 weeks)

| Claim today | Evidence today | Work |
|---|---|---|
| Agent injection jitter P50 0.28ms vs ~45ms `docker exec` (README, site, 2.1) | None in the repo | Add `strife bench inject` (or a Go benchmark), plus a `live.yml` job that applies N partition/heal transitions through each transport and records P50/P95/max from kernel-side timestamps. Publish runner spec + numbers; replace the table with whatever it measures. |
| PostgreSQL, Kafka, TiDB targets (2.3) | Fake-server unit tests | `live.yml` jobs mirroring etcd/Valkey: a single Postgres primary under `kill` → `--expect linearizable`; reads from an async standby → `--expect violation`; TiDB `txn` workload at `serializable`; Kafka `queue` model with broker kills. |
| cgroup faults `freeze`, `io-stall` (1.3) | Command-level unit tests | GitHub's `ubuntu-24.04` runners use cgroup v2, which the development sandbox lacks. Validate both faults there via the agent. |
| Checker 120k–260k ops/s, ~1.5ms diagnosis (1.4) | `pkg/checker/bench_test.go` | `docs/BENCHMARKS.md`: machine, Go version, exact command, before/after table, known limits. Optionally a nightly job that flags a >20% regression. |
| Compliance mapping (4.2) | Unit tests | Control IDs such as `SEOJK-29/MRTIK-DRC` are Strife's own labels, not official clause numbers. Cite the source regulation and clause for each, state that the mapping is informative, and get it reviewed by one practising auditor before selling it. |

Done when: every number on the README and the site links to a job or a
command that reproduces it.

### 5.1 Clients that work outside the examples ⬜

The clients only connect to clusters configured for the examples:

- **PostgreSQL:** only `trust` auth. PostgreSQL 14+ defaults to
  `scram-sha-256`, so add SCRAM-SHA-256 (stdlib `crypto/hmac` + `pbkdf2`) and
  TLS (`sslmode=require|verify-full`).
- **MySQL:** only `mysql_native_password`. MySQL 8 defaults to
  `caching_sha2_password`, and MySQL 8.4/9 disable native auth by default.
  Add caching_sha2 (full auth over TLS or RSA) and TLS.
- **Kafka:** fixed v0 APIs. Kafka 4.0 removed Produce v0–2 and Fetch v0–3
  (KIP-896), so the example only works because it pins 3.7. Add
  `ApiVersions` negotiation and Produce/Fetch at v3+ (record batches).
- **All clients:** TLS and credentials from env/files, never from
  `strife.json` in plain text (it lands in reports and attestations).

Done when: each client passes its `live.yml` job against the current major
version with default authentication.

### 5.2 Distribution and stable contracts ⬜

- **Releases:** tag `v0.2.0`. Cross-compiled binaries (linux/darwin/windows ×
  amd64/arm64) with SHA-256 checksums, an SBOM, and a signature made with
  Strife's own `attest` (dogfooding 4.1).
- **Action:** download the release binary instead of building (saves ~15s per
  job), pin by tag, then list it on the GitHub Marketplace.
- **Container image** on `ghcr.io` for `strife` and `strife-agent`.
- **Versioned schemas** (`schemas/report.v1.json`, `history.v1.json`,
  `dossier.v1.json`) with a compatibility test. Enterprises archive these
  files for years, so the format is a contract.
- **CHANGELOG** and semver from here on.

### 5.3 Engine depth ⬜

- **Elle list-append workload:** the `txn` checker works on registers. With
  list-append, every read reveals the full version order, which is what
  makes Elle find G1c/G2 reliably without guessing write order. This is the
  biggest checker gap compared with Jepsen.
- **Live-run shrinking** (carried over from 1.1): re-run a reduced schedule
  against a real cluster N times and report the result as a *candidate*
  with its reproduction rate, never as a proof.
- **Kubernetes target:** run the agent as a DaemonSet, inject faults by pod
  label, and validate on `kind` in CI. Most enterprise databases run on
  Kubernetes, so this reaches the most users.

### 5.4 Correctness safety net ⬜

- Fuzz tests (`go test -fuzz`) for every wire-protocol parser (RESP,
  Postgres, MySQL, Kafka), the Wasm decoder, and `history.jsonl` parsing.
  These parse untrusted bytes.
- A cross-check of the `txn` checker against brute force on small random
  histories, the same way `TestAgainstBruteForce` guards the linearizability
  checker.
- A determinism check for Wasm workloads in CI (`--determinism` with
  `--workload`).

## Phase 6: Commercial layer (after 5.0–5.2)

- **Design partners:** 2–3 Indonesian fintechs or banks (the `bi-ojk`
  mapping is the hook) running Strife on staging. Success means one dossier
  used in a real audit.
- **Cloud runner (3.3, not built yet):** hosted campaigns against a
  customer's staging, with a run history, regression alerts and dossiers
  signed with a per-tenant key. Start single-tenant, and with the
  customer's own runner (like a self-hosted GitHub runner) so their data
  never leaves their network.
- **Pricing experiment:** free CLI and Action. Paid plans cover hosted
  campaigns, signed dossiers and support SLAs.

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
