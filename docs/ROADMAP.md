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
| `strife-shrink` (delta debugging) | ⏳ Phase 1 |
| `strife-netlink` | ⏳ Phase 2, as part of `strife-agent` |
| eBPF probes, Wasm workloads | ⏳ Phase 2+, scope narrowed (see below) |
| Rust core daemon | ❎ Deferred: Go already meets the single-binary, zero-dependency goal |

## Phase 1: MVP completion (open source, Apache 2.0)

Goal: close the gaps in the blueprint's Phase 1 and ship the strongest
differentiator early.

### 1.1 `strife shrink`: automatic minimisation ⭐ top priority

Once a violation is found, Strife reduces the fault schedule to a minimal set
that still triggers it.

- **Simulator first.** It is deterministic, so delta debugging (ddmin) over the
  fault list is sound: drop a subset, re-run the same seed, and keep the drop
  if the violation remains.
- Then shrink further:
  - shorten each fault's window,
  - shrink the target set (fewer nodes),
  - reduce the workload length.
- Output: `schedule.min.json` plus a report comparing the original and the
  minimal result.
- **Caveat:** dropping a fault changes the randomness downstream of it (more
  or fewer messages). The criterion is "still violates", not "the same
  violation". The report must state this.
- **Done when:** the three buggy systems shrink to ≤ 3 faults on at least 80%
  of their violating seeds, with a test.
- Live runs: possible later (re-run with a reduced schedule), but
  probabilistic. Mark the result as a "candidate", not a proof.

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
| "Minimal 3-step scenario" (before 1.1 is measured) | The shrink success rate from the 1.1 test |

## Monetisation

This follows the blueprint and stays consistent with the current license:

- **Free, Apache 2.0:** the engine (simulator, checker, nemesis, CLI, agent)
  remains 100% open, to build reputation and community.
- **Commercial:** the visual analyzer, the cloud runner, and signed compliance
  reports for banking, fintech and database vendors.
