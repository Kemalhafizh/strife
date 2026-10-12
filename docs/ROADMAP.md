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
| Register / KV spec model | ✅ Done (`kv`, `set`) |
| HTML violation reporter | ✅ Done (HTML, Markdown, JSON, fingerprint) |
| JUnit reporter | ⏳ Phase 1 |
| Standalone CLI | ✅ Done (`sim`, `check`, `plan`, `run`, `systems`) |
| etcd target | ✅ Client and example. ⏳ Not yet validated on a real Docker host |
| Redis target | ⏳ Phase 1 |
| Raft demo | ✅ Done, with three bug switches that Strife catches |
| `strife-shrink` (delta debugging) | ✅ Done in Phase 1.1: `strife shrink`, measured below |
| `strife-netlink` | ⏳ Phase 2, as part of `strife-agent` |
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
- **History shrinking:** cut the counterexample down to the handful of
  operations that conflict, not just the schedule.

### 1.2 JUnit output and a GitHub Action

- `--junit report.xml` for `sim`, `check` and `run`: one test case per key or
  seed, with the counterexample as the failure message.
- A reusable `action.yml` (download the binary, run the gauntlet, upload the
  report as an artifact).

### 1.3 Redis / Valkey client and real validation

- A RESP client in the standard library. CAS via `EVAL` (Lua), reads via
  `GET`.
- Expected result: single-node Redis with async replicas **is not**
  linearizable after failover. That makes a good demo, not a failure of
  Strife.
- Run `examples/etcd` on a real Docker host and record the result in the
  README.

### 1.4 Checker hardening

- A time limit per key (`--check-timeout`) next to `--max-steps`.
- A more targeted counterexample: show the latest completed write that
  contradicts the stuck read.
- A benchmark suite (`go test -bench`) with numbers published in the README,
  replacing the blueprint's "millions of events in seconds" claim.

## Phase 2: Extensibility (open-source core)

### 2.1 `strife-agent` with netlink

The blueprint is right that shell-based injection carries overhead. Most of it
comes from `docker exec` and ssh, not from `iptables` itself. The fix with the
biggest impact is architectural:

- **A small agent per node** (inside the node's network namespace). It
  receives commands over a socket and applies them immediately. It is
  idempotent and reports back what was actually applied.
- **Netlink inside the agent:** qdisc / netem via `RTM_NEWQDISC`, cgroup v2
  via direct file writes. The packet filter goes through nftables over
  netlink, which is the most complex part, so it comes last. Shell
  `iptables` stays the fallback.
- Measure **injection jitter** before and after, and publish the numbers.
- Language: Go for consistency. Revisit only if a hard requirement appears
  that Go cannot meet.

### 2.2 New models

- FIFO queue (Kafka-like), unique-id generator, counter.
- **Transactional anomaly detection** (Elle-style cycle detection): checks
  serializability and snapshot isolation for PostgreSQL, CockroachDB and TiDB.
  This is the main prerequisite for those targets.

### 2.3 New targets

- PostgreSQL (Patroni), Kafka, TiDB. Each one needs a client with honest
  error classification and a docker-compose example.

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

## Phase 3: Cloud runner (open-core SaaS)

- An interactive timeline analyzer: zoom, filter per process and per key, and
  overlay the counterexample on the faults.
- A cloud runner: thousands of seeds in parallel, a dashboard of results per
  commit, and regression tracking ("this seed used to pass").
- Integration with the GitHub Action from 1.2.

## Phase 4: Compliance (enterprise license)

- **Signed reports** (Sigstore / in-toto attestation) on top of the existing
  fingerprint.
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
