---
title: Resilience-test evidence for IT audits in banking and fintech
description: Strife maps fault-injection results to controls that cite DORA, SOC 2, ISO 27001 and Indonesia's POJK 11/2022 and SEOJK 29/2022, and binds them with an Ed25519 signature anyone can verify.
eyebrow: Compliance & audit
lead: "`strife compliance` turns fault-injection results into a dossier that maps each finding to controls citing regulation, with the evidence, the gaps and the limits of the method."
translationKey: compliance
---

## What you get

- **A print-ready dossier** (HTML, Markdown and JSON) from a single test report or a multi-seed campaign.
- **Every control graded from empirical evidence:** PASS, PARTIAL (fault coverage or signing incomplete) or FAIL, with the gap and how to close it.
- **A “Methodology & Scope Boundaries” section** in every dossier: what was tested, which fault classes were left out, and how deterministic simulation differs from a live cluster.
- **A cryptographic signature.** Reports are signed with Ed25519 (in-toto v0.1) using your team's key, and anyone can re-verify them, including on the [verification page](/verify) without installing anything.

## Controls for Indonesian regulation

The control IDs and citations below are read from the engine, so they always match the dossier.

{{< controls framework="bi-ojk" >}}

## Other frameworks

{{< controls framework="other" >}}

## The flow

1. **Test.** Run `strife sim` for simulation, `strife run` against a staging cluster, or `strife campaign` across many seeds.
2. **Sign.** Create a key pair for your QA or audit team with `strife keygen`, then sign the report with `strife sign`.
3. **Assemble the dossier.** Run `strife compliance` with the frameworks you need.
4. **Verify.** The auditor checks the signature with `strife verify` or in the browser.

```sh
strife compliance --report strife-out/raft-seed7/report.json \
  --bundle strife-out/raft-seed7/bundle.json --key audit-key.pub \
  --frameworks dora,soc2,iso27001,bi-ojk --out strife-compliance --strict
```

See a [sample dossier](/demos/dossier.html) (simulated data) and the full guide in the [attestation documentation](/docs/attestation).

## The limits, stated plainly

> [!IMPORTANT]
> A dossier is **technical evidence for an auditor, not a certification, not a legal opinion, and not a statement by any regulator**. Control IDs are Strife's labels for its technical checks, not numbers issued by a regulator. Each control names the provision it supports, and the mapping is informative.

- **Simulation** proves an algorithm's behaviour deterministically and replays bit for bit, but it tests a model of the system, not your production system.
- **Live-cluster tests** produce evidence about your system but cannot replay exactly: the fault schedule and the workload are identical, the timing is not. The dossier states which mode was used.
- **Finding no violation does not prove a system is bug-free.** It means none was found within the faults and duration tested, and the dossier records that scope.

## Take it into your next audit

We run a small design partner program for banks and fintechs that want to run Strife on staging and take their first dossier into a real audit. [Talk to us](/contact).
