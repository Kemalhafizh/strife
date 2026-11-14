# Security Policy

Strife is an open-source project built with a minimal, auditable supply chain: **zero external Go dependencies** (`stdlib` only), deterministic builds, and no outbound telemetry.

## Supported Versions & LTS Security Maintenance

Strife `v1.x` is the active **Long-Term Support (LTS)** release line. Security fixes and soundness patches are applied to the `1.0.x` / `1.x` release series and the `main` branch for at least **24 months** from the initial `v1.0.0` release (through **November 2028**), with a minimum **6-month advance notice** prior to End-of-Life (EOL):

| Version | Supported | Maintenance Commitment |
| :--- | :--- | :--- |
| `1.0.x` (LTS) / `main` | **Yes** | Active security advisories, parser/transport hardening, and backported soundness fixes (through at least November 2028) |
| `< 1.0.0` (`0.x`) | **No** | Pre-1.0 development series; upgrade to `v1.0.0` or newer |

## Scope & Threat Model Boundaries

The following classes of issues are treated as security vulnerabilities:

1. **`strife-agent` Authentication & Privilege Boundaries (`pkg/agent`, `cmd/strife-agent`)**
   - Bypass of Bearer token authentication (`subtle.ConstantTimeCompare`), non-loopback listener restrictions (`-insecure-no-token` aside), path/PID validation on `/apply`, `/exec`, `/probes`, or command injection via agent endpoints.
2. **`strife serve` Run Registry & Job Queue HTTP Daemon (`pkg/serve`)**
   - Bypass of Bearer token checks on `POST /api/v1/ingest`, `POST /api/v1/jobs`, `POST /api/v1/jobs/claim`, or `POST /api/v1/jobs/<id>/complete`.
   - Unauthorized disclosure of `live_config` payloads on unauthenticated `GET /api/v1/jobs` requests, cross-worker job completion spoofing, or replay of already-completed jobs.
   - Unbounded memory allocation beyond the request body limits (4 MiB for job creation, 32 MiB for report/campaign ingestion), stored HTML/script injection (XSS) in the embedded dashboard or compliance dossier views, or leakage of webhook URLs/tokens via `/api/v1/webhooks` or logs.
   - *Deployment note:* `strife serve` speaks plaintext HTTP over TCP (default `127.0.0.1:8080`). When exposing the registry beyond loopback, terminate TLS in front of `strife serve` and keep `--token` enabled.
3. **`strife worker` Pull-Based Remote Worker (`pkg/serve/worker.go`)**
   - Unbounded response allocation when polling or claiming jobs from a registry endpoint, or credential leakage from `live_config` into persisted report artifacts (`runner.Config.Redacted()` must redact all credentials before report serialization).
4. **Untrusted Input Parsers (`pkg/client`, `pkg/history`, `pkg/workload`)**
   - Panics, out-of-bounds reads, infinite loops, or unbounded memory/stack exhaustion when parsing malformed wire-protocol frames (PostgreSQL, MySQL, RESP, Kafka, etcd), `history.jsonl`, Jepsen `.edn` histories (`maxEDNDepth`), OpenTelemetry Span JSON (`ExportTraceServiceRequest` in `pkg/history/otel.go`), or `.wasm` modules.
5. **WebAssembly VM Sandbox & Report Escaping (`pkg/workload`, `pkg/report`, `pkg/compliance`, `pkg/campaign`)**
   - Escape from the fuel-metered (`DefaultMaxFuel`) stack machine, call-stack depth cap, or linear memory bounds during custom `.wasm` execution.
   - Unescaped HTML/JS injection into `report.html`, `campaign.html`, or `dossier.html` from untrusted history fields.
6. **Cryptographic Attestation & Release Verification (`pkg/attest`, `pkg/compliance`)**
   - Ed25519 signature verification bypass, canonical digest collision/malleability in `report.json` / `bundle.json` / `dossier.json`, or tampering that goes undetected by `strife verify`.
7. **Secret Redaction (`internal/secret`, `pkg/serve`)**
   - Leakage of resolved `env:NAME` or `file:PATH` credentials or webhook query tokens into generated report, campaign, or registry artifacts.

## Out of Scope

The following are intentional design behavior or operational prerequisites rather than security vulnerabilities:

- **Simulated buggy implementations:** Systems such as `raft-stale-reads`, `raft-lease`, `raft-unsafe-fsync`, `raft-ledger-overdraft`, `2pc-ledger-torn-commit`, and `outbox-dual-write` intentionally inject protocol flaws for verification testing.
- **Host privilege requirements for kernel fault injection:** Injecting `iptables`, `tc netem`, or `cgroup v2` faults on live clusters inherently requires local root privileges, `CAP_NET_ADMIN`, or Docker host access on the test nodes. Zero-privilege fault injection without host capabilities uses `transport.type: "proxy"`.
- **Self-inflicted local DoS via CLI flags:** Passing multi-gigabyte local files directly to offline CLI commands without a trust boundary unless accompanied by an unbounded-allocation bug in a parser under test.

## Reporting a Vulnerability

**Please do not open a public GitHub issue for suspected security vulnerabilities.**

Report vulnerabilities privately through:

1. **GitHub Private Vulnerability Reporting (Preferred):**
   Use the [**Report a vulnerability**](https://github.com/Kemalhafizh/strife/security/advisories/new) button under the repository's **Security** tab.
2. **Email:**
   Contact the maintainer directly at `muhammadkemalhafizh@gmail.com` with `[SECURITY] strife` in the subject line.

Please include the affected package/subcommand, version (`strife version`) or commit hash, impact summary, and a minimal reproducer (command line, `history.jsonl` / `.edn`, or fuzz crasher).

## Response & Disclosure Process

- **Acknowledgment:** Within **72 hours** of receiving a report.
- **Assessment & Triage:** Within **7 days**, with confirmation of reproduction and severity.
- **Coordinated Disclosure:** A fix, regression test (or fuzz corpus case), and advisory release note will be coordinated with the reporter before public disclosure. Critical issues may receive an out-of-band patch release.

## Verifying Release Integrity

Release artifacts provide two layers of verification in [`.github/workflows/release.yml`](.github/workflows/release.yml):

1. **Unconditional Checksums & SPDX SBOM (Every Release):**
   Every GitHub Release unconditionally publishes `SHA256SUMS` covering all platform archives (`linux`, `darwin`, `windows` on `amd64`/`arm64`) together with a machine-readable SPDX 2.3 Software Bill of Materials (`strife_<version>.spdx.json`).
2. **Cryptographic Ed25519 `in-toto` Attestation (When Signing Key Is Configured):**
   When the repository's `STRIFE_RELEASE_KEY` secret is configured in the release environment, the workflow signs all archives, `SHA256SUMS`, and the SPDX SBOM into an `in-toto` v0.1 Statement bundle (`strife_<version>.intoto.json`) verifiable against `release.pub`.

```sh
# 1. Verify archive SHA-256 digests (available on all releases)
sha256sum -c --ignore-missing SHA256SUMS

# 2. Verify cryptographic Ed25519 in-toto provenance bundle (when strife_<version>.intoto.json is attached)
strife verify --bundle strife_<version>.intoto.json --key release.pub --release-dir ./downloaded-release
```

Client-side browser verification is also available at [https://kemalhafizh.github.io/strife/verify/](https://kemalhafizh.github.io/strife/verify/) and runs purely in WebAssembly with no uploads or telemetry.
