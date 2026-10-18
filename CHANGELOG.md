# Changelog

All notable changes to Strife. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). The JSON outputs in
[`schemas/`](schemas/) are versioned contracts: within a major version they
only gain optional fields.

## [Unreleased]

### Added
- The `kubernetes-agent` transport and `examples/kubernetes/strife-agent.yaml`:
  strife-agent runs as a DaemonSet, and each fault goes to the agent on the
  target pod's node, which applies it inside the pod: partitions and netem
  in its network namespace, pause and kill on its processes only, freeze
  and io-stall on its cgroup. The pods need no privileges or tools, every
  fault kind works, and no node helper is needed. In kind: 1.1–1.9 ms per
  network or cgroup fault and 2.8 ms per signal at P50, against 130–230 ms
  through `kubectl exec`.
- strife-agent requests can name a target container (`target.container_id`);
  `-cgroup-root`, `-io-root` and `-require-target` configure an agent that
  serves a whole node. A kill is healed by restarting the database inside
  the container (nsenter, with the container's environment, working
  directory and credentials, cloned into its cgroup).

### Changed
- Agent transports skip the shell setup (an iptables chain in every node)
  and, when the agents healed everything, the shell teardown.
- `strife bench inject` labels rows by transport, so `agent` and
  `kubernetes-agent` no longer both read `agent:direct`.
- The `kubernetes-agent` transport looks up every pod's node and container
  during setup, so the first fault lands on time (a benchmark's first
  partition took 329 ms) and a missing pod fails the setup.
- The live Kubernetes check through the DaemonSet runs seed 710, which
  draws all seven fault kinds, a kill among them, within 60 s.

### Fixed
- `campaign.html` and `dossier.html` no longer overflow narrow screens: their
  tables scroll inside their box (at 375 px they overflowed by ~300 px).

### Documentation
- The order of `--faults` is part of a run's identity: the generator picks
  kinds by position, so one seed gives different schedules for
  `clock-skew,partition` and `partition,clock-skew`.

### Security
- strife-agent restarts a killed database with the container init's user,
  groups, capability sets and `no_new_privs`, not as root with every
  capability.
- With `-require-target`, strife-agent stays off the node: no network
  faults on containers that use the node's network, no cgroups outside the
  target container's, no scripts (`/exec`, shell fallback), and teardown
  leaves the node's interface and packet filter alone.
- strife-agent removes a root qdisc only if it is netem: healing or teardown
  no longer deletes a qdisc it did not install.
- The live CI's agent tokens are random instead of derived from the public
  run ID.

## [0.2.0] - Unreleased

Phases 5 and 5.5 of the [roadmap](docs/ROADMAP.md): every published claim
now has a job that reproduces it, the clients work against current database
versions with their default security, every fault kind lands natively, and
Strife ships as signed releases. Phase 6 starts with a self-hosted run
registry.

### Added
- `strife server`: a self-hosted run registry for CI. It verifies every
  push (report fingerprint, history digest, Ed25519 signature against the
  project's trusted keys) and refuses what does not verify, compares each
  run with the previous run of its series (campaigns seed by seed), records
  regressions and fixes, alerts a Slack-compatible webhook, and serves a
  dashboard, the original artifacts, the report timeline and compliance
  dossiers generated on demand. Per-project write and read tokens, retention,
  no database. See [`docs/server.md`](docs/server.md).
- `strife push` sends a run directory, a `report.json` or a `campaign.json`
  to the registry with the commit, branch and CI job link, and fails the job
  on a regression (or on any violation, or never). The Action gains
  `push-server`, `push-project` and `push-token-env`.
- `freeze` and `io-stall` on Kubernetes, through a node helper DaemonSet
  (`examples/kubernetes/node-helper.yaml`, `transport.node_helper`): the
  pod's cgroup and its data disk are found on its node from the container
  ID. Validated in kind: etcd stays linearizable under both.
- `strife bench inject` measures any transport of the config, Kubernetes
  included.
- `testdata/anomalies`: histories of anomalies found on real systems, with
  their provenance, checked by the test suite. The first is a G2 among three
  `SERIALIZABLE` transactions on PostgreSQL 17.11 whose commits raced within
  0.2 ms; replayed one statement at a time, PostgreSQL aborts them correctly.
- `strife bench inject`: measures how late faults land on a real cluster,
  per transport and fault kind, and for strife-agent also the time spent
  inside the agent; the live CI publishes the numbers, collected in
  [`docs/BENCHMARKS.md`](docs/BENCHMARKS.md).
- `strife shrink --live`: delta debugging of a live run's fault schedule on
  the real cluster, several attempts per candidate, reported as a candidate
  reproducer with its measured reproduction rate. `strife run --schedule`
  replays a recorded or shrunk schedule.
- Kubernetes transport (`"type": "kubernetes"`): faults through `kubectl
  exec`, `"ip": "auto"` resolved to pod IPs, `{ip}` endpoints expanded per
  node; `examples/kubernetes` and a kind job in CI.
- List-append transactions checked the way Elle does (`--model append`,
  live `workload.model: append`): G0, G1a/b/c, G-single, G2, G-realtime,
  lost appends, incompatible orders, phantom, duplicate and internal reads.
  Live append runs end with a final read of every key.
- Clients: PostgreSQL SCRAM-SHA-256/MD5 and TLS; MySQL/TiDB
  caching_sha2_password (fast and full authentication) and TLS; Kafka with
  ApiVersions, leader routing and v2 record batches (3.x and 4.x); Redis
  AUTH and TLS; list-append transactions for PostgreSQL, MySQL/TiDB, Redis,
  etcd and Kafka. `db.password` and `transport.token` accept `env:NAME` and
  `file:PATH`, and literal secrets are redacted from reports.
- `db.txn_isolation` and `db.init_sql` for SQL targets.
- Examples validated in CI: PostgreSQL 17, MySQL 8.4, TiDB 8.5, Kafka 3.9
  and 4.1, etcd transactions, etcd on Kubernetes, cgroup v2 faults,
  strife-agent. Known-weak configurations must be caught: PostgreSQL write
  skew and stale standby reads, MySQL and TiDB-pessimistic REPEATABLE READ.
- Compliance controls carry a `citation` of the provision they support.
- Docker transport: `sudo` for host commands, `"cgroup": "auto"` and
  `"io_device": "auto"` discovery.
- Release pipeline: archives for Linux, macOS and Windows, SHA256SUMS, an
  SPDX SBOM, an in-toto release attestation signed with Strife's own
  `strife sign --release`, and images `ghcr.io/kemalhafizh/strife` and
  `strife-agent`. The GitHub Action downloads the verified binary of its tag.
- JSON Schemas for `report.json`, `history.jsonl` and `dossier.json`, with a
  test that refuses incompatible changes.
- Fuzzing of every parser of untrusted bytes; brute-force cross-checks of
  both transaction checkers, including a completeness measurement for
  list-append.

### Changed
- strife-agent applies partitions through nf_tables over netlink, IPv4 and
  IPv6, without forking a binary: 1.1–1.2 ms to inject and 1.0–1.3 ms to
  heal at P50 end to end (4.02 ms and 7.44 ms through `iptables`), ~145 µs
  inside the agent. A heal inserts an accept rule instead of deleting (a deletion
  waits for an RCU grace period, ~14 ms), and the chain is compacted when
  idle. `iptables` (IPv4) remains the fallback where nf_tables is missing.
- strife-agent confirms signal faults the way it confirms freezes: a pause
  returns once `/proc` shows every process stopped, a resume once none is,
  a kill once each has exited (at most 5 s, then an error). A signal is
  delivered when its target is next scheduled, so before, a pause could be
  reported before the process had stopped.
- strife-agent sets up its packet filter on the first partition instead of
  at start, and reports the filter it uses (`filter:nftables`,
  `filter:iptables`) in its capabilities and in each fault's method.
- The linearizability search tries candidates earliest deadline first and
  only one per class of interchangeable operations. Single-register
  histories with 50 concurrent processes, which exhausted the default budget
  and answered `unknown`, now finish (10,000 ops at 110 k ops/s); histories
  with 20 processes check 2–4× faster.
- Splitting a history by key copies each operation once, and the search
  keeps its candidate lists on one stack: 5-process histories check
  1.4–1.9× faster, a diagnosed violation is proved ~3× sooner, and a
  100,000-operation check allocates 100 MB instead of 392 MB.
- Compliance control IDs are now Strife labels (`dora.data-integrity`,
  `soc2.recovery`, `bi-ojk.jejak-audit`…) instead of strings that looked
  like official clause numbers. Scripts that match on the old IDs must be
  updated.
- strife-agent refuses to listen on a network address without a token.
- Runs record when each fault landed (every node confirmed), not only when
  dispatch started; reports summarise the fault timing.
- A freeze is reported only once `cgroup.events` shows the cgroup frozen.
- A live run in which no operation succeeded is `unknown`, not a pass.
- The register transaction checker only infers version order from real time
  at strict serializability, and reads results from completions.

### Fixed
- strife-agent reported partitions as applied on hosts without `iptables`
  (no rule was installed) and always advertised `iptables`; such a partition
  now fails with an error.
- Agent "jitter" statistics were Unix timestamps, not durations, and the
  published 0.28 ms figure had no benchmark behind it; it is replaced by
  measurements.
- strife-agent encoded netem parameters in the wrong field order and units,
  so `loss` faults timed out.
- The DORA data-integrity control cited "Art. 9.4(c)"; the provision is
  Art. 9(3)(b)–(c).
- PostgreSQL standbys in the example could fail to clone the primary when a
  checkpoint recycled WAL mid-copy.
- PostgreSQL transactions attributed rows to the wrong read after an empty
  SELECT.
- The workload isolation level never reached the SQL clients.
- A malformed Wasm workload could crash Strife; it now traps.
- The Kafka and TiDB examples could not start.
- Runs prepared iptables on every node even without partition faults.

## [0.1.0]

The engine: deterministic simulator with Raft and three bug variants, the
WGL linearizability checker with diagnoses and minimal witnesses, the
seeded nemesis (partition, latency, loss, kill, pause, freeze, io-stall,
clock skew), live runs against etcd and Redis/Valkey, `strife shrink`,
JUnit and job-summary output with a GitHub Action, strife-agent, server
probes, Wasm workloads, campaigns with regression tracking, in-toto report
attestation and compliance dossiers.

[0.2.0]: https://github.com/kemalhafizh/strife/compare/22a3dff...main
[0.1.0]: https://github.com/kemalhafizh/strife/tree/22a3dff
