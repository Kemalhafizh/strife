# Changelog

All notable changes to Strife. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). The JSON outputs in
[`schemas/`](schemas/) are versioned contracts: within a major version they
only gain optional fields.

## [0.2.0] - Unreleased

Phase 5 of the [roadmap](docs/ROADMAP.md): every published claim now has a
job that reproduces it, the clients work against current database versions
with their default security, and Strife ships as signed releases.

### Added
- `strife bench inject`: measures how late faults land on a real cluster,
  per transport and fault kind; the live CI publishes the numbers.
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
  and 4.1, etcd transactions, cgroup v2 faults, strife-agent.
- Docker transport: `sudo` for host commands, `"cgroup": "auto"` and
  `"io_device": "auto"` discovery.
- Release pipeline: archives for Linux, macOS and Windows, SHA256SUMS, an
  SPDX SBOM, an in-toto release attestation signed with Strife's own
  `strife sign --release`, and images `ghcr.io/kemalhafizh/strife` and
  `strife-agent`. The GitHub Action downloads the verified binary of its tag.
- JSON Schemas for `report.json`, `history.jsonl` and `dossier.json`, with a
  test that refuses incompatible changes.
- Fuzzing of every parser of untrusted bytes; brute-force cross-checks of
  both transaction checkers.

### Changed
- strife-agent refuses to listen on a network address without a token.
- Runs record when each fault landed (every node confirmed), not only when
  dispatch started; reports summarise the fault timing.
- A freeze is reported only once `cgroup.events` shows the cgroup frozen.
- A live run in which no operation succeeded is `unknown`, not a pass.
- The register transaction checker only infers version order from real time
  at strict serializability, and reads results from completions.

### Fixed
- Agent "jitter" statistics were Unix timestamps, not durations.
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

[0.2.0]: https://github.com/kemalhafizh/strife/compare/main...phase-5/proof-and-reach
[0.1.0]: https://github.com/kemalhafizh/strife/tree/22a3dff
