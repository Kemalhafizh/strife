---
title: Security & data handling
description: How Strife handles your data and credentials, its software supply chain, the release key, and how to report a vulnerability.
eyebrow: Trust
lead: Answers to what security and procurement teams usually ask before installing a new tool.
translationKey: security
---

## Your data

- **It runs on your infrastructure.** The CLI and `strife-agent` run on your own machines and network. Strife only talks to the targets you configure: database endpoints, agents, Docker, SSH or Kubernetes. There is no telemetry and no connection to our servers.
- **Target credentials** are given as `env:NAME` or `file:PATH` references, so they never land in configuration files or reports.
- **The agent** refuses to listen beyond loopback without a token, and compares tokens in constant time.
- **The tools on this site** (playground, studio, verifier) run the engine as WebAssembly in your browser. The files you choose are not uploaded anywhere.
- **This site** uses no cookies and no third-party trackers.

## Supply chain

- **Zero external dependencies.** The engine builds from the Go standard library alone, and `go.mod` has no `require` block. All the code that runs can be reviewed in one repository.
- **Verifiable releases.** Every release ships archives for Linux, macOS and Windows (amd64 and arm64), `SHA256SUMS`, an SPDX SBOM and multi-architecture container images. When the release key is configured, it also ships an in-toto attestation signed with Ed25519.
- **Continuous fuzzing** of every parser of untrusted bytes, meaning the database protocols and WebAssembly. Findings are recorded in the [benchmarks](/docs/benchmarks#3-checker-correctness).

## Release key

{{< release-key >}}

Verify the files you downloaded on the [verification page](/verify), or with `strife verify --release-dir`.

## Reporting a vulnerability

{{< security-contact >}}

## License

Strife is released under the [Apache License 2.0](https://github.com/Kemalhafizh/strife/blob/main/LICENSE): you may use, modify and distribute it, commercially included.
