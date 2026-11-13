# Security Policy

Strife is an open-source project built with a minimal, auditable supply chain: **zero external Go dependencies** (`stdlib` only), signed release artifacts (`Ed25519` + `in-toto` v0.1), and no outbound telemetry.

## Supported Versions

Security fixes are applied to the latest release series and the `main` branch:

| Version | Supported |
| :--- | :--- |
| `0.7.x` / `main` | Yes |
| `< 0.7.0` | No |

## Scope

The following classes of issues are treated as security vulnerabilities:

1. **`strife-agent` Authentication & Privilege Boundaries (`pkg/agent`, `cmd/strife-agent`)**
   - Bypass of Bearer token authentication (`subtle.ConstantTimeCompare`), non-loopback listener restrictions (`-insecure-no-token` aside), path/PID validation on `/apply`, `/exec`, `/probes`, or command injection via agent endpoints.
2. **`strife serve` Run Registry HTTP Daemon (`pkg/serve`)**
   - Bypass of Bearer token checks on `POST /api/v1/ingest`, unbounded memory allocation beyond the 32 MiB ingest body limit, stored HTML/script injection (XSS) in the embedded dashboard or compliance dossier views, or leakage of webhook URLs/tokens via `/api/v1/webhooks` or logs.
   - *Deployment note:* `strife serve` speaks plaintext HTTP over TCP (default `127.0.0.1:8080`). When exposing the registry beyond loopback, terminate TLS in front of `strife serve` and keep `--token` enabled.
3. **Untrusted Input Parsers (`pkg/client`, `pkg/history`, `pkg/workload`)**
   - Panics, out-of-bounds reads, infinite loops, or unbounded memory/stack exhaustion when parsing malformed wire-protocol frames (PostgreSQL, MySQL, RESP, Kafka, etcd), `history.jsonl`, Jepsen `.edn` histories (`maxEDNDepth`), or `.wasm` modules.
4. **WebAssembly VM Sandbox & Report Escaping (`pkg/workload`, `pkg/report`, `pkg/compliance`, `pkg/campaign`)**
   - Escape from the fuel-metered (`DefaultMaxFuel`) stack machine, call-stack depth cap, or linear memory bounds during custom `.wasm` execution.
   - Unescaped HTML/JS injection into `report.html`, `campaign.html`, or `dossier.html` from untrusted history fields.
5. **Cryptographic Attestation & Release Verification (`pkg/attest`, `pkg/compliance`)**
   - Ed25519 signature verification bypass, canonical digest collision/malleability in `report.json` / `bundle.json` / `dossier.json`, or tampering that goes undetected by `strife verify`.
6. **Secret Redaction (`internal/secret`, `pkg/serve`)**
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

Every release ships with `SHA256SUMS`, an SPDX SBOM, and (when configured) an `in-toto` Ed25519 attestation bundle. Verify downloaded releases with:

```sh
sha256sum -c --ignore-missing SHA256SUMS
strife verify --bundle strife_<version>.intoto.json --key release.pub --release-dir ./downloaded-release
```

Client-side browser verification is also available at [https://kemalhafizh.github.io/strife/verify/](https://kemalhafizh.github.io/strife/verify/) and runs purely in WebAssembly with no uploads or telemetry.
