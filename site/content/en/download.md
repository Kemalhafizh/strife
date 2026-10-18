---
title: Download Strife
description: Download the Strife CLI for Linux, macOS and Windows, install it with one command, and check the files are genuine.
eyebrow: Download
lead: One program file, no dependencies. Free and open source under the Apache 2.0 license.
layout: download
translationKey: download
verify_text: Every release ships `SHA256SUMS`, an SPDX SBOM and a release attestation signed with Ed25519. Check the checksums, and when you need certainty that we built the files, verify the attestation with the official release key.
---

This needs Go 1.23 or later. The commands above fetch the code straight from the repository and compile it on your machine. No third-party dependencies are downloaded along with it.

## System requirements

- **Simulation, history checking, shrinking and verification** run on Linux, macOS and Windows (amd64 and arm64).
- **Real clusters.** The CLI injects faults into the target nodes over Docker, SSH, Kubernetes (`kubectl exec`) or the `strife-agent`. Targets and the agent need Linux. Network partitions use iptables and `tc netem`; freeze and I/O stalls use cgroup v2.
- Next steps are in the [Quick Start](/docs/quick-start), with example setups for etcd, PostgreSQL, MySQL/TiDB, Valkey, Kafka and Kubernetes in the [documentation](/docs).
