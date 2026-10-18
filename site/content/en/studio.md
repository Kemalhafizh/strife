---
title: History studio
description: Check a history.jsonl from your own system for linearizability or a transaction isolation level, right in the browser. Nothing is uploaded.
eyebrow: In-browser tool
lead: Already recording operations in your own tests? Drop the file here. Strife checks it with the same checker as `strife check` and shows the interactive report.
layout: studio
translationKey: studio
empty: Choose a history.jsonl file, or try one of the samples on the left.
---

## File format

One operation per line (JSONL): each call is recorded as an `invoke`, then its outcome as `ok`, `fail` or `info` (unknown). Times are nanoseconds since the start of the test.

```json
{"process":1,"type":"invoke","f":"write","key":"k0","value":2,"time":1000000}
{"process":1,"type":"ok","f":"write","key":"k0","value":2,"time":5000000}
{"process":3,"type":"invoke","f":"read","key":"k0","time":9000000}
{"process":3,"type":"ok","f":"read","key":"k0","value":2,"time":12000000}
```

The `kv` model fits read/write/compare-and-set registers. For multi-key transactions use `txn` or `append` with the isolation level you want to prove. Each model is described in [Consistency Models](/docs/models). Very large histories check faster with the CLI; in the browser the engine runs several times slower.
