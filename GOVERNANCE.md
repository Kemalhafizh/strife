# Project Governance

**Strife** is an open-source distributed systems testing, verification, and audit-evidence toolkit licensed under the [Apache License 2.0](LICENSE). This document defines how the repository is governed, how technical decisions are made, and what invariants every change must uphold.

---

## Maintainer Model

Strife uses a **maintainer-led ("benevolent maintainer")** governance model suitable for a focused verification engine where correctness and determinism depend on coherent architectural oversight.

### Roles

| Role | Responsibilities | Current Assignment |
| :--- | :--- | :--- |
| **Creator & Lead Maintainer** | Architecture, release approvals, security advisories, determinism/soundness sign-off, and governance updates | [@Kemalhafizh](https://github.com/Kemalhafizh) (Muhammad Kemal Hafizh) |
| **Area Reviewers** *(emerging)* | Code review and triage in specific subsystems (`pkg/checker`, `pkg/txn`, `pkg/client`, `pkg/agent`, `site`) invited as sustained contributions merit | Assigned per review |
| **Contributors** | Issue reports, minimal `history.jsonl` / `--seed` reproducers, tests, fuzz regression inputs, bug fixes, and documentation | Community |

Ownership routing is enforced through [`.github/CODEOWNERS`](.github/CODEOWNERS). Contributors who build sustained ownership in a subsystem may be invited to act as co-maintainers or area reviewers by the Lead Maintainer.

---

## Technical Decision-Making

Most changes—bug fixes, regression tests, documentation improvements, fuzz seeds, and performance optimizations that preserve existing contracts—are reviewed directly through pull requests following [`CONTRIBUTING.md`](CONTRIBUTING.md).

### Proposals Requiring Design Discussion First

Open an issue describing the motivation, invariants, and compatibility impact before writing large implementations for any of the following:

1. **Output Schemas & External Contracts**
   - Modifying [`schemas/report.v1.json`](schemas/report.v1.json), [`schemas/history.v1.json`](schemas/history.v1.json), or [`schemas/dossier.v1.json`](schemas/dossier.v1.json).
   - Changing CLI exit codes (`0` passed/matches `--expect`, `1` violation/mismatch, `2` usage/config error, `3` `unknown` inconclusive verdict, `4` runtime/transport error).
   - Changing the WebAssembly 1.0 workload plugin ABI (`next_op`) or the `https://strife.dev/attestation/v1` `in-toto` predicate structure.
2. **Core Dependency Policy**
   - `go.mod` maintains **zero external dependencies** (Go standard library only). Any proposal suggesting exceptions in optional sub-modules must justify isolation and build-time boundary separation (`site/wasm` is the existing standalone Go module solely for the browser WebAssembly bridge).
3. **Simulator Determinism & Checkers**
   - Modifying `internal/rng`, `pkg/sim`, `pkg/nemesis`, or `pkg/explore` in ways that alter existing seeded `trace_hash` / `schedule_hash` outputs.
   - Changing soundness boundaries in `pkg/checker`, `pkg/txn`, `pkg/model`, or `pkg/shrink` (`isSoundHistory` and brute-force cross-checks).
4. **New Live Targets, Transports & Compliance Frameworks**
   - Adding database protocol clients (`pkg/client`), fault injection transports (`pkg/transport`, `pkg/agent`), or regulatory control mappings (`pkg/compliance`).

---

## Non-Negotiable Repository Invariants

Pull requests are rejected if they violate any of the following gates verified in [`.github/workflows/ci.yml`](.github/workflows/ci.yml) and [`.github/workflows/pages.yml`](.github/workflows/pages.yml):

1. **Zero External Dependencies in Core:** [`go.mod`](go.mod) has no `require` block.
2. **Bit-for-Bit Simulator Determinism:** `--determinism` sweeps verify identical execution and `trace_hash` across replays for fixed seeds.
3. **Honest Verdicts & Evidence Boundaries:** Checkers never return `linearizable` when search budgets expire (`unknown`, exit `3`); compliance dossiers state tested/untested scope and never claim regulatory certification.
4. **Measured Documentation Claims:** Quantitative claims added to `README.md` or docs must match reproducible runs documented in [`docs/BENCHMARKS.md`](docs/BENCHMARKS.md) and pass [`site/scripts/check-wording.sh`](site/scripts/check-wording.sh) and [`site/scripts/check-links.cjs`](site/scripts/check-links.cjs).
5. **Schema Backward Compatibility:** `go test ./schemas` (`TestCompatible`) prevents breaking changes within v1 schemas.
6. **Live Cluster Validation:** Live database client changes must work against real targets (not only mock servers) and preserve [`examples/`](examples) + [`.github/workflows/live.yml`](.github/workflows/live.yml) assertions.

---

## Versioning, Compatibility & Releases

Strife follows [Semantic Versioning](https://semver.org/):

- **Pre-1.0 (`v0.Y.Z`):**
  - Additive CLI flags, new simulated systems, new models, and optional JSON fields ship in minor (`v0.Y.0`) releases while preserving backward compatibility for existing CLI workflows and `v1` schemas.
  - Bug fixes, hardening, and security patches ship in patch (`v0.Y.Z`) or minor releases as appropriate.
- **Deprecations:** Any planned removal or behavior change of a documented CLI flag or HTTP API field is announced in [`CHANGELOG.md`](CHANGELOG.md) under `[Unreleased]` / release notes at least one release ahead of removal unless required to resolve a critical soundness or security defect.
- **Release Pipeline ([`.github/workflows/release.yml`](.github/workflows/release.yml)):**
  - Triggered by pushing annotated tag `vX.Y.Z` matching an entry `## [X.Y.Z]` in [`CHANGELOG.md`](CHANGELOG.md) and `Version` in [`internal/version/version.go`](internal/version/version.go).
  - Publishes static binaries (`linux`, `darwin`, `windows` on `amd64`/`arm64`), `SHA256SUMS`, an SPDX SBOM, Ed25519 `in-toto` release attestations (`strife sign --release`), and multi-arch container images on `ghcr.io`.

---

## Open-Core Scope

Everything in this repository—the deterministic simulator, `strife` CLI, `strife-agent`, linearizability/transactional/ledger/settlement checkers, live protocol clients, `strife serve` run registry daemon, WebAssembly workload VM, Ed25519 attestation tooling, compliance dossier generator, and static website—is open source under the [Apache License 2.0](LICENSE).

Should optional managed SaaS offerings (such as hosted multi-cluster orchestration or commercial support SLAs described in [`docs/ROADMAP.md`](docs/ROADMAP.md)) be offered separately, the core engine and verification tools in this repository remain Apache 2.0.

---

## Security & Conduct

- **Vulnerabilities:** Follow private disclosure in [`SECURITY.md`](SECURITY.md). Security advisories are coordinated privately and disclosed alongside patched releases.
- **Community Standards:** Participation in issues, pull requests, and reviews is governed by [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).
- **Support Channels:** Usage help, reproducibility guidelines, and links are in [`SUPPORT.md`](SUPPORT.md).
