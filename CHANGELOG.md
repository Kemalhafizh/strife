# Changelog

All notable changes to Strife. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). The JSON outputs in
[`schemas/`](schemas/) are versioned contracts: within a major version they
only gain optional fields.

## [Unreleased]

### Fixed

- Harden checker soundness, input bounds (wire decoders, JSONL/EDN parsers, and Wasm module loader), live client outcome classification guards, and live fault handling ([#13](https://github.com/Kemalhafizh/strife/pull/13)).

### Documentation & Governance

- Add project governance (`GOVERNANCE.md`), `NOTICE`, `.github/CODEOWNERS`, Dependabot configuration, expanded `SECURITY.md`, updated contributor verification workflow (`CONTRIBUTING.md`), and aligned `README.md`, `docs/`, and `examples/` documentation with `v0.7.0`.

## [0.8.0] - 2026-11-14

### Added (0.8.0)

- Pull-based remote worker (`strife worker`) and job queue API (`POST /api/v1/jobs`, `POST /api/v1/jobs/claim`, `POST /api/v1/jobs/{id}/complete`, `GET /api/v1/jobs`) on `strife serve`, enabling private-VPC and isolated staging clusters to execute simulation, multi-seed campaign, and live database runs without inbound network ports.
- Zero-dependency OpenTelemetry Span JSON trace importer (`pkg/history/otel.go`), automatically detected by `strife check` alongside Strife JSONL and Jepsen EDN histories.
- Quantitative resilience SLO gates (`--max-mttr`, `--min-availability`) on `strife sim`, `strife check`, `strife run`, and `strife ci-report`, plus time-series resilience trend tracking (`GET /api/v1/trends`) in `strife serve`.
- Native CI Pull Request gate and comparative Markdown digest generator (`strife ci-report`) comparing consistency verdicts, MTTR/availability deltas, campaign regressions, and regulatory compliance controls.

## [0.7.0] - 2026-11-07

### Added (0.7.0)

- Per-tenant Ed25519 signing keys (`--tenant-keys`), public key discovery
  (`GET /api/v1/keys`), and multi-tenant Time-Series SLA Overview dashboard in
  `strife serve` (`pkg/serve`):
  - `--tenant-keys tenantA=keyA.key,tenantB=keyB.key` signs ingested reports
    with tenant-specific Ed25519 keys (`strife-serve:<tenant>`) and verifies
    them automatically on `GET /api/v1/runs/<id>/dossier`.
  - `GET /api/v1/keys` exposes active Ed25519 public verification keys in PKIX
    PEM format (`public_key_pem`, `key_id`, `tenant`, `signer`).
  - The embedded web dashboard (`GET /`) now renders a `Target & Tenant
    Time-Series SLA Overview` table and supports `?tenant=` filtering.
- Quantitative SLA/MTTR regulatory control gates (`--max-mttr`,
  `--min-availability`) and SARIF 2.1.0 output (`STRIFE-COMPLIANCE-*`) in
  `strife compliance` (`pkg/compliance`, `pkg/report/sarif.go`).
- Campaign-level availability and post-fault recovery (`avg_availability`,
  `mttr_p50_ms`, `mttr_p95_ms`, `mttr_max_ms`) aggregation and multi-model
  violation diagnosis (`ledger`, `settlement`) in `strife campaign`
  (`pkg/campaign`).
- GitHub Action (`action.yml`, `scripts/github-action.sh`) support for
  `sarif`, `push`, `push-token`, and `tenant` inputs and `sarif` output.

## [0.6.0] - 2026-11-07

### Added (0.6.0)

- Multi-tenant registry isolation, time-series SLA analytics, and built-in
  push client in `strife serve` (`pkg/serve`):
  - Per-tenant campaign baseline isolation (`tenant` field / `?tenant=` query
    parameter on `/api/v1/ingest`, `/api/v1/runs`, and `/api/v1/series`).
  - `GET /api/v1/series` aggregates per-(tenant, target) time-series metrics
    (`pass_rate`, `avg_availability`, `latest_mttr_p95_ms`, `max_mttr_p95_ms`,
    and chronological `points`).
  - `serve.Push` and `--push` / `--push-token` / `--tenant` flags on
    `strife check` push reports directly to a `strife serve` registry.
- SARIF 2.1.0 output (`--sarif <file>` in `pkg/report/sarif.go`): maps
  linearizability, transactional (`G0`, `G1a/b/c`, `G2`, `G2-item`,
  `G-realtime`), double-entry ledger (`LEDGER-*`), settlement (`SETTLEMENT-*`),
  and campaign regression violations to structured SARIF rules for GitHub Code
  Scanning and security dashboards.
- Bidirectional Jepsen EDN interoperability (`history.WriteEDN` in
  `pkg/history/edn.go` and `strife check --export-edn <file>`): losslessly
  exports Strife histories (including `kv`, `append`, `ledger`, and
  `settlement` transactions) into canonical Jepsen `.edn` format.
- Interactive `Resilience & Post-Fault Recovery (MTTR)` section in
  `report.html` (`pkg/report/html.go`): renders overall, healthy-window, and
  fault-window availability alongside P50/P95/Max MTTR and per-fault recovery
  timelines.

## [0.5.0] - 2026-11-07

### Added (0.5.0)

- Live cluster settlement & transactional-outbox reconciliation (`workload.model:
  "settlement"` in `strife run` with post-heal atomic `finalSettlementAudit`,
  idempotent `MemoryStore` settlement appends, and
  `examples/postgres/strife-settlement.json`).
- Sound transactional history shrinking (`strife shrink --history` in
  `pkg/shrink/history.go`): preserves producer-consumer micro-operation
  integrity across `txn`, `append`, `ledger`, and `settlement` histories (JSONL
  and Jepsen EDN) and reduces read-only audit transactions in the `pure-reads`
  phase.
- Half-duplex / post-request response-drop fault injection in `pkg/transport`
  (`Proxy.DropResponseAfterRequest` and `Proxy.ForwardedBytes`) to deterministically
  trigger indeterminate (`info`) client outcomes after an operation has already
  been applied on the upstream server.
- On-demand compliance dossier generation and SLA breach alerting in
  `strife serve`:
  - `GET /api/v1/runs/<id>/dossier` (supports `?format=html` and
    `?frameworks=dora,soc2,iso27001,bi-ojk`) evaluates regulatory compliance
    dossiers directly from stored registry runs and automatically verifies
    stored Ed25519 `in-toto` signature bundles.
  - `--max-mttr` and `--min-availability` flags on `strife serve` dispatch
    `report.sla_breach` webhook alerts when post-heal recovery latency (MTTR P95)
    or service availability violates SLA thresholds.
  - `RunEntry` in `registry.jsonl` now indexes `overall_availability`,
    `fault_window_availability`, `mttr_p50_ms`, `mttr_p95_ms`, and `mttr_max_ms`.
- Coverage-guided fuzz target `FuzzReadEDN` in `pkg/history` and browser
  WebAssembly Studio support for Jepsen `.edn` histories and the
  `outbox-dual-write` preset.

## [0.4.0] - 2026-11-07

### Added (0.4.0)

- User-space L4 TCP fault-injection proxy (`transport.type: "proxy"` in
  `pkg/transport/proxy.go`): injects `partition`, `latency`, `loss`, `kill`,
  `pause`, `freeze`, and `io-stall` across Linux, macOS, and Windows without
  root privileges, `iptables`, `tc netem`, or `cgroup v2`.
- `strife doctor` CLI command (`pkg/transport/doctor.go`): inspects host OS,
  root privileges, `cgroup v2`, local CLI tools (`docker`, `kubectl`, `ssh`,
  `tc`, `iptables`, `proxy-l4`), and TCP reachability of cluster endpoints.
- Quantitative resilience and post-heal recovery latency (`MTTR`) metrics in
  `report.json`, `report.md`, and compliance dossiers (`overall_availability`,
  `healthy_window_availability`, `fault_window_availability`, `mttr` P50/P95/Max,
  and per-fault `recoveries`).
- Cross-system settlement and transactional-outbox consistency model
  (`--model settlement`, simulated `outbox-settlement` and `outbox-dual-write`):
  proves `SETTLEMENT-ATOMICITY`, `SETTLEMENT-NO-ORPHAN-CREDIT`, and
  `SETTLEMENT-IDEMPOTENCY`.
- Native Jepsen `.edn` history parser (`pkg/history/edn.go`) with automatic
  format detection in `history.Read` so `strife check` and `strife shrink`
  accept exported Jepsen EDN logs directly.

## [0.3.0] - 2026-11-07

### Added (0.3.0)

- Live cluster double-entry ledger auditing (`workload.model: "ledger"` in
  `strife run`): executes concurrent account-to-account transfers (`acct:i ->
  acct:j`) and full-supply balance audits across PostgreSQL (`SELECT ... FOR
  UPDATE` / configured `txn_isolation`), MySQL/TiDB, etcd v3 (`mod_revision`
  optimistic transactions), and Redis/Valkey (atomic Lua scripts), followed by
  a post-healing final balance audit (`examples/postgres/strife-ledger.json`).
- Simulated two-phase commit ledger systems (`2pc-ledger` and
  `2pc-ledger-torn-commit`): models multi-phase prepare/commit coordination and
  catches non-atomic Saga / 2PC coordinator bugs where the debit leg commits
  before the credit leg (`LEDGER-CONSERVATION` violations under `partition` and
  `kill`).
- Webhook alerting in `strife serve` (`--webhook <urls>` and `GET
  /api/v1/webhooks`): automatically dispatches Slack, Discord, or generic JSON
  notifications when an ingested report violates consistency
  (`report.violation`) or an ingested campaign contains violations or seed
  regressions (`campaign.regression` / `campaign.violation`).

### Changed (0.3.0)

- Direct Serialization Graph (`pkg/txn`) construction now uses $O(1)$
  per-vertex edge deduplication and interval-order transitive reduction for
  real-time (`ro`) and write-write (`ww`) edges, accelerating large
  strict-serializable ledger and transactional checks by ~10x.
- `kvPruner` in `pkg/checker` keeps indeterminate (`info`) writes behind
  definite (`ok`) candidates in the Wing & Gong / Lowe linearizability search
  order so multi-client chaos histories resolve without exhausting step budgets.

## [0.2.0]

Phase 5 of the [roadmap](docs/ROADMAP.md): every published claim now has a
job that reproduces it, the clients work against current database versions
with their default security, and Strife ships as signed releases.

### Added (0.2.0)

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
- Double-entry financial ledger and conservation-of-money checker (`--model
  ledger`, simulated systems `raft-ledger` and `raft-ledger-overdraft`):
  proves `LEDGER-CONSERVATION` ($\sum B_i = M_0$), `LEDGER-OVERDRAFT` ($B_i <
  0$ and concurrent `G2-item` write skew), `LEDGER-DOUBLE-SPEND` across
  indeterminate `info` timeout retries, and `LEDGER-UNBALANCED` ($\sum
  \Delta_k \ne 0$), integrated with the `bi-ojk.rekonsiliasi-buku-besar`
  compliance control.
- Value-interval pruning (`kvPruner`) in the Wing & Gong / Lowe
  linearizability solver: propagates transitive effective `[effCall,
  effReturn]` windows across sole-creator writes and CAS transitions, solving
  10,000 single-key operations across 50 concurrent processes in 22,913 steps
  (~534 ms).
- Native Linux `AF_NETLINK` FIB policy routing partitioner (`RTM_NEWRULE` /
  `RTM_DELRULE` with `FR_ACT_BLACKHOLE`) in `strife-agent`, with automatic
  fallback to `iptables`.
- Kubernetes host-level cgroup v2 and Pod `/proc/<pid>/ns/net` discovery in
  `strife-agent` (`pod:<ns>/<name>`, `k8s:<name>`, `container:<id>`) and a
  DaemonSet manifest (`deploy/kubernetes/strife-agent-daemonset.yaml`).
- `strife serve` ([`docs/server.md`](docs/server.md)): self-hosted continuous
  verification and run registry HTTP daemon with an append-only artifact
  store, REST API (`/api/v1/ingest`, `/api/v1/runs`, `/api/v1/health`),
  automatic seed-level baseline regression diffing, optional Ed25519
  `in-toto` signing on ingest, and an embedded web UI.
- Bilingual client website (`site/`, Indonesian and English) with in-browser
  WebAssembly playground, history studio, and cryptographic attestation
  verifier.

### Changed (0.2.0)

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

### Fixed (0.2.0)

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

[Unreleased]: https://github.com/Kemalhafizh/strife/compare/v0.7.0...HEAD
[0.7.0]: https://github.com/Kemalhafizh/strife/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/Kemalhafizh/strife/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/Kemalhafizh/strife/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/Kemalhafizh/strife/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/Kemalhafizh/strife/compare/640c9bc...v0.3.0
[0.2.0]: https://github.com/Kemalhafizh/strife/compare/22a3dff...640c9bc
[0.1.0]: https://github.com/Kemalhafizh/strife/tree/22a3dff
