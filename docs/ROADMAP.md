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
| Cloud runner & regression dashboard | 🟡 Self-hosted registry (`strife server`) in Phase 6.1; the hosted option is next |
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
  - Records, per transition, the round trip and the time spent inside the agent.

**Measured injection latency** (`strife bench inject`, 50 transitions per row,
GitHub-hosted runner, 3-node etcd; method and full table in
[`BENCHMARKS.md`](BENCHMARKS.md#1-fault-injection-latency)):

| Fault | `docker exec` P50 | `strife-agent` P50 | Inside the agent (P50) |
|---|---|---|---|
| pause | 43.7 ms | 1.36 ms | 372 µs |
| kill | 44.4 ms | 1.58 ms | 495 µs |
| partition | 57.9 ms | 4.02 ms | 2.61 ms (still runs `iptables`) |
| latency | 42.8 ms | 48.1 ms (reply delayed by the fault itself) | 159 µs |

The earlier table here (P50 0.28 ms, ">50x") had no benchmark behind it and
was computed from Unix timestamps; it is withdrawn. The measured gain is ~30x
for signals and netem, ~14x for partitions.

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
    - **EU DORA** (`dora`): `dora.resilience-testing` (Art. 24–25), `dora.data-integrity` (Art. 9(3)(b)–(c)), `dora.recovery` (Art. 11–12).
    - **AICPA SOC 2** (`soc2`): `soc2.processing-integrity` (PI1.3–PI1.4), `soc2.recovery` (A1.2–A1.3), `soc2.test-evidence` (CC4.1).
    - **ISO/IEC 27001:2022** (`iso27001`): `iso27001.redundancy` (Annex A 8.13–8.14), `iso27001.clock-sync` (8.17), `iso27001.security-testing` (8.29).
    - **OJK** (`bi-ojk`): `bi-ojk.pemulihan-bencana`, `bi-ojk.integritas-data`, `bi-ojk.jejak-audit` (POJK No. 11/POJK.03/2022; SEOJK No. 29/SEOJK.03/2022).
  - Control IDs are Strife's labels; each control carries a `citation` of the provision it supports (since Phase 5).
  - Evaluates each control as `PASS`, `PARTIAL` (with explicit remediation hints when fault coverage or cryptographic signing is incomplete), or `FAIL`.
- **Documented Methodology & Scope Boundaries**:
  - Explicitly states *What Is Tested*, *What Is Not Tested* (omitted fault classes, Byzantine/hardware bit-rot, organizational policies), and *Execution & Reproducibility Boundaries* (simulation bit-for-bit replay vs. live cluster interleavings), plus a clear auditor disclaimer.
- **Audit Dossier Artifacts & CLI (`strife compliance`)**:
  - Generates tamper-evident `dossier.json`, `dossier.md`, and a self-contained, print-ready `dossier.html` from either a single `report.json` or a multi-seed `campaign.json` (plus optional `bundle.json` + Ed25519 public key).

## Phase 5: Proof, reach, distribution ✅ done (PR #8)

An audit of `main` at `22a3dff` (2026-10-17) found feature coverage running
ahead of evidence: live CI covered only etcd and Valkey, the new clients were
tested against fake servers only, and one headline number had no benchmark
behind it. Phase 5 paid that debt first, then widened reach. Every number
below is reproduced by a command in [`BENCHMARKS.md`](BENCHMARKS.md).

### 5.0 Pay the proof debt ✅

| Claim | Evidence now |
|---|---|
| Agent injection latency | `strife bench inject` + the `agent` job: pause 1.36 ms, kill 1.58 ms, partition 4.02 ms at P50 vs 43–58 ms for `docker exec`; the agent-side column separates kernel time (150–500 µs for signals and netem) from the confirmation. The unbacked 0.28 ms table is withdrawn. |
| PostgreSQL, Kafka, TiDB, MySQL targets | `live.yml` jobs for each, with expected passes and expected violations (G2 at REPEATABLE READ, stale standby reads, MySQL and TiDB-pessimistic REPEATABLE READ not being snapshot isolation, Kafka stale reads after leader changes with no lost records). |
| cgroup faults | `cgroup` job on cgroup v2 runners: `io.max` verified to stall writes, freeze confirmed through `cgroup.events`, etcd linearizable under both; freeze 9.6 ms and io-stall 8.3 ms P50. |
| Checker throughput | `BENCHMARKS.md` §2: 95k–508k ops/s on one register depending on concurrency; list-append 41k–57k txns/s; the 50-process single-key case is `unknown`, documented as the limit. |
| Compliance mapping | Control IDs renamed to Strife labels (`dora.data-integrity`…), each with a `citation` of the provision it supports (DORA Art. 9(3)(b)–(c) replaces the incorrect "9.4(c)"); dossiers say the mapping is informative. |

### 5.1 Clients that work outside the examples ✅

- **PostgreSQL:** SCRAM-SHA-256 (RFC 7677 test vectors), MD5, TLS
  (`require`, `verify-full` with a CA), per-statement results, ambiguous-error
  classification; validated on PostgreSQL 17 with its default auth.
- **MySQL / TiDB:** `caching_sha2_password` (fast path; full auth over TLS or
  RSA-OAEP), `mysql_native_password`, TLS, `init_sql`; validated on MySQL 8.4
  and TiDB 8.5.
- **Kafka:** ApiVersions, Metadata v4, Produce v3, Fetch v4, RecordBatch v2
  with CRC-32C; validated on Kafka 3.9.1 and 4.1.0 (KRaft).
- **Redis / Valkey:** `AUTH`, TLS, Lua list-append transactions.
- **All clients:** credentials as `env:NAME` / `file:PATH`, redacted in
  reports and attestations; the agent token likewise, compared in constant
  time; the agent refuses a non-loopback listener without a token.

### 5.2 Distribution and stable contracts ✅ (tagging is the release step)

- `release.yml`: on a `v*` tag, binaries for linux/darwin/windows ×
  amd64/arm64, `SHA256SUMS`, an SPDX SBOM, an in-toto release attestation
  made with `strife sign --release` (when `STRIFE_RELEASE_KEY` is set), and
  multi-arch `ghcr.io` images for `strife` and `strife-agent`.
- The Action downloads and checksum-verifies the release binary when pinned
  to a tag, and builds from source otherwise.
- `schemas/report.v1.json`, `history.v1.json`, `dossier.v1.json` with
  validation of real outputs and a compatibility test that refuses breaking
  changes (it caught one during this phase).
- `CHANGELOG.md`; `strife version` stamped at build time.

### 5.3 Engine depth ✅

- **Elle-style list-append** (`workload.model: "append"`, `--model append`):
  version orders from reads, ww/wr/rw and real-time edges, G0–G2, G-single,
  G-realtime, lost appends, incompatible orders, duplicates and internal
  consistency; live on etcd, PostgreSQL, MySQL, TiDB, Valkey and Kafka.
- **Live-run shrinking** (`strife shrink --live`, `strife run --schedule`):
  delta debugging over the fault schedule with N attempts per candidate,
  reported as a candidate with its reproduction rate; CI shows the
  PostgreSQL standby anomaly needs no fault at all.
- **Kubernetes target:** the `kubernetes` transport drives pods through
  `kubectl exec`, discovers pod IPs (`"ip": "auto"`) and expands `{ip}`
  endpoints; `examples/kubernetes` runs etcd as a StatefulSet and the
  `kubernetes` job validates it in kind under partition, kill, pause,
  latency and loss. Host faults on Kubernetes came in 5.5.

### 5.4 Correctness safety net ✅

- Fuzz targets for RESP, PostgreSQL, MySQL and Kafka decoders, the Wasm
  decoder, `history.jsonl`, and the list-append checker's soundness, run in
  CI; they found a Wasm stack-underflow panic and a MySQL integer overflow.
- Brute-force cross-checks: list-append 0 / 150,030 valid histories flagged,
  99.81 % of 49,970 invalid ones caught; register transactions 0 / 40,000.
  The cross-check exposed, and the rewrite fixed, the register checker adding
  real-time edges at plain serializability.
- Wasm workload determinism is checked in CI.

### 5.5 Native fault paths ✅ (PR #9)

Measured in [`BENCHMARKS.md`](BENCHMARKS.md) §1–§3, run
[39054819049](https://github.com/Kemalhafizh/strife/actions/runs/39054819049).

| Carried forward | Done |
|---|---|
| Partitions forked `iptables` (4.02 ms inject, 7.44 ms heal at P50) | nf_tables over netlink, IPv4 and IPv6, one batch per change: 1.1–1.2 ms inject and 1.0–1.3 ms heal end to end over two runs, ~145 µs inside the agent. Heals insert an accept instead of deleting (a deletion waits ~14 ms for an RCU grace period); the chain is compacted when idle. `scripts/live/check-agent-partition.sh` proves the traffic really stops and resumes. `iptables` stays as the fallback. |
| `freeze` and `io-stall` did not work on Kubernetes | A node helper DaemonSet finds the pod's cgroup and data disk on its node; etcd in kind stays linearizable under both. ~90 ms per transition, all of it `kubectl exec`. |
| One key, ~50 concurrent processes: `unknown` after 50 M steps | Earliest-deadline-first candidates, one per class of interchangeable operations: 10,000 ops with 50 processes at 110 k ops/s, 100,000 at 18 k ops/s; 20 processes 2–4× faster. Brute force agrees on 400,000 random histories across four models. Splitting histories by key no longer copies each operation several times, which made easy histories 1.4–1.9× faster. |

Found on the way: the agent reported partitions as applied on hosts without
`iptables` and always advertised it. It now fails such a partition and
reports the filter it actually uses.

### 5.6 Still open

- One practising auditor reviews the compliance citations before the
  mapping is sold.
- Tag `v0.2.0`, set `STRIFE_RELEASE_KEY`, list the Action on the
  Marketplace.

### 5.7 strife-agent as a Kubernetes DaemonSet ✅ (phase-6/k8s-agent)

Measured in [`BENCHMARKS.md`](BENCHMARKS.md) §1 and §5, runs
[39109099995](https://github.com/Kemalhafizh/strife/actions/runs/39109099995)
and [39123114580](https://github.com/Kemalhafizh/strife/actions/runs/39123114580).

| Carried forward | Done |
|---|---|
| Kubernetes faults took 130–230 ms each through `kubectl exec` and needed tools (iptables, tc) and `NET_ADMIN` in every pod | One agent per node applies each fault inside the target pod: nf_tables and netem in its network namespace, signals on its processes only, freeze and io-stall on its cgroup. 1.1–1.9 ms per network or cgroup fault, 2.8 ms per signal, 4.5 ms for a restart, at P50 in kind. Pods need no privileges or tools. A 60 s etcd run whose schedule draws all seven kinds, a kill and its in-pod restart included, stays linearizable (14,221 operations). |
| An agent sharing the node's namespaces could fault the node itself | `-require-target` refuses faults that name no pod, network faults on `hostNetwork` pods, cgroups outside the target, and every script (`/exec`, the shell fallback). Teardown leaves the node's interface and packet filter alone, and netem deletion names its kind. |
| A restart after a kill ran as root with every capability | The agent's own static binary enters the container and takes on its init's user, groups, capability sets and `no_new_privs` before it starts `db.start`; it never inherits the agent's environment. |

Found on the way: deleting netem removed whatever root qdisc an interface
had, and the CI's agent tokens were derived from the public run ID.

## Phase 6: Commercial layer (in progress)

The blueprint's cloud runner, built in the order customers can adopt it:
first something they run themselves, inside their network, that CI feeds.

### 6.1 Self-hosted run registry (`strife server`) ✅ foundation (PR #9)

- **Ingest that verifies:** `strife push` (and the Action's `push-server`)
  sends a report with its history and attestation bundle, or a campaign
  manifest. The server recomputes the report fingerprint, hashes the
  history against the report and checks the Ed25519 signature against the
  project's trusted keys; anything that does not verify is refused (`422`),
  never stored with a warning. A project can require signatures.
- **Regression tracking:** runs form series (kind, target, fault kinds);
  each run is compared with the previous one of its series and recorded as
  a regression, a fix, still failing or still passing. Campaigns are
  compared seed by seed. `strife push` fails the CI job on a regression.
- **Alerts:** a Slack-compatible webhook on regressions and fixes.
- **Reading:** a dashboard (no JavaScript, strict CSP), the JSON API, the
  original artifacts, the interactive report, and compliance dossiers
  generated on demand with the signature re-verified.
- **Operations:** one binary, a directory instead of a database, writes are
  atomic, per-project write and read tokens compared in constant time,
  retention, TLS, the same container image. The `action` CI job pushes a
  campaign to a live server on every commit and checks the API and the
  dashboard.

### 6.2 Next

- **Design partners:** 2–3 Indonesian fintechs or banks (the `bi-ojk`
  mapping is the hook) running Strife on staging with the registry. Success
  means one dossier used in a real audit.
- **Registry depth:** signed campaigns ✅ (`strife sign --campaign`, the
  Action's `sign-key-env`, verified at push and in dossiers), SSO (OIDC) instead of shared tokens,
  object storage for artifacts, trend charts per series, and scheduled
  campaigns run by a customer-side runner (like a self-hosted GitHub
  runner) so data never leaves their network.
- **Hosted option:** the same registry multi-tenant, with per-tenant signing
  keys, for teams that do not want to run it.
- **Pricing experiment:** free CLI, Action and self-hosted registry for one
  project. Paid plans cover multiple projects with SSO, signed dossiers,
  the hosted option and support SLAs.

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
