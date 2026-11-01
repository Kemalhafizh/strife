# Security Policy

Strife is an open-source project built with a minimal, auditable supply chain: **zero external Go dependencies** (`stdlib` only), signed release artifacts (`Ed25519` + `in-toto` v0.1), and no outbound telemetry.

## Supported Versions

Security fixes are applied to the latest release series and the `main` branch:

| Version | Supported |
| :--- | :--- |
| `0.2.x` / `main` | Yes |
| `< 0.2.0` | No |

## Scope

The following classes of issues are treated as security vulnerabilities:

1. **`strife-agent` Authentication & Privilege Boundaries (`pkg/agent`, `cmd/strife-agent`)**
   - Bypass of Bearer token authentication (`subtle.ConstantTimeCompare`), non-loopback listener restrictions, or command injection via agent endpoints.
2. **Untrusted Input Parsers (`pkg/client`, `pkg/history`, `pkg/workload`)**
   - Panics, out-of-bounds reads, infinite loops, or unbounded memory allocation when parsing malformed wire-protocol frames (PostgreSQL, MySQL, RESP, Kafka, etcd), `history.jsonl` traces, or `.wasm` modules.
3. **WebAssembly VM Sandbox (`pkg/workload`)**
   - Escape from the fuel-metered stack machine or linear memory bounds during custom workload execution.
4. **Cryptographic Attestation & Verification (`pkg/attest`, `pkg/compliance`)**
   - Signature verification bypass, canonical digest collision/malleability in `report.json` / `bundle.json`, or tampering that goes undetected by `strife verify`.
5. **Secret Redaction (`internal/secret`)**
   - Leakage of resolved `env:NAME` or `file:PATH` credentials into generated report artifacts.

## Reporting a Vulnerability

**Please do not open a public GitHub issue for suspected security vulnerabilities.**

Report vulnerabilities privately through:

1. **GitHub Private Vulnerability Reporting (Preferred):**
   Use the [**Report a vulnerability**](https://github.com/Kemalhafizh/strife/security/advisories/new) button under the repository's **Security** tab.
2. **Email:**
   Contact the maintainer directly at `muhammadkemalhafizh@gmail.com` with `[SECURITY] strife` in the subject line.

Please include the affected package/subcommand, version or commit hash, and a minimal reproducer (command line, input file, or fuzz crasher).

## Verifying Release Integrity

Every release ships with `SHA256SUMS`, an SPDX SBOM, and an `in-toto` Ed25519 attestation bundle. Verify downloaded releases with:

```sh
strife verify --release-dir ./downloaded-release --key release.pub
```
