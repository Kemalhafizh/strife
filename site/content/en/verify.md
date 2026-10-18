---
title: Verify test evidence and release files
description: Check the Ed25519 signatures on Strife test evidence and on release files in your browser, without installing or uploading anything.
eyebrow: For auditors
lead: Evidence you cannot check yourself is only a claim. This page runs `strife verify` in your browser, so an auditor can confirm a report has not changed since it was signed.
layout: verify
translationKey: verify
empty: Choose a bundle and a public key on the left, then press “Verify”.
---

## What is checked

**Test evidence (`bundle.json`).** The in-toto v0.1 statement written by `strife sign` carries the verdict, the seed, the report fingerprint and the SHA-256 of the operation history. Verification confirms the Ed25519 signature matches the public key you hold. Every detail shown comes from the signed payload, never from the unsigned copy inside the file. If you also choose the `history.jsonl`, this page recomputes its hash to confirm it is the history that was signed.

**Release files.** Each Strife release can carry an attestation that signs the hash of every archive, `SHA256SUMS` and the SBOM. Choose the attestation and the files you downloaded. This page checks the signature, then compares the hash of each file.

## Where the public key comes from

Obtain the public key through a channel you trust, such as directly from the team that signed the report, not from the same place as the evidence. The result shows the key's fingerprint so it can be confirmed by phone or official letter. Strife's official release key is announced on the [Security](/security) page.

The same verification is available in the CLI:

```sh
strife verify --bundle bundle.json --key audit.pub --history history.jsonl
strife verify --bundle strife_v0.2.0.intoto.json --key release.pub --release-dir .
```
