---
title: Try Strife in your browser
description: Run Strife's simulator and checker in your browser, with nothing to install. Pick a real bug scenario, see the violation, then replay it with the CLI.
eyebrow: Playground
lead: The same engine as the CLI, compiled to WebAssembly. Pick a scenario, run it, and compare its execution fingerprints with the native engine. Nothing is sent to a server.
layout: try
translationKey: try
empty: Pick one of the scenarios on the left, or choose your own settings and press “Run the simulation”.
---

## What happens when you press the button

1. Your browser runs the same `strife sim` as the CLI release: a five-node Raft cluster simulated in one process, with the network, disks, timers and clocks all driven by the seed.
2. Every client operation is recorded, then formally checked for an order that explains all of the results (linearizability).
3. The same seed always produces the same execution. For the ready-made scenarios, this page compares the hashes of the run in your browser with those the native engine produced on Linux when the site was built.

The order of the fault list is part of the schedule, so the CLI command in the result uses exactly the order you chose. To test your own system, use the CLI against a staging cluster: see the [Quick Start](/docs/quick-start) and [Live Clusters](/docs/live-clusters).
