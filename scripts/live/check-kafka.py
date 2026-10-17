#!/usr/bin/env python3
"""Asserts what the Kafka live test may and may not find.

Kafka does not promise linearizable reads: a fetch from a leader that was
just elected, or that has not yet learned it was deposed, can miss records
already acknowledged with acks=all. Those show up as G-realtime cycles and
are tolerated. Anything else fails the job: a lost acknowledged record
(lost-append), reordering or divergence (incompatible-order), duplicates,
phantom records, or a final read that never succeeded.
"""
import glob
import json
import sys

outcome, out_dir = sys.argv[1], sys.argv[2]
reports = glob.glob(f"{out_dir}/*/report.json")
if outcome not in ("passed", "violation") or len(reports) != 1:
    sys.exit(f"unexpected outcome {outcome!r} with reports {reports}")
report = json.load(open(reports[0]))
kinds = set()
for key in report["check"]["keys"]:
    kinds |= set((key.get("counterexample") or {}).get("anomalies") or [])
if outcome == "violation" and not kinds:
    sys.exit("violation without a listed anomaly")
unexpected = kinds - {"G-realtime"}
if unexpected:
    sys.exit(f"anomalies beyond stale reads: {sorted(unexpected)}")

# Every key with an acknowledged append must have a successful final read.
history = [json.loads(line) for line in open(reports[0].replace("report.json", "history.jsonl"))]
final_proc = 1 << 30
pending, appended, final_ok = {}, set(), set()
for op in history:
    if op["type"] == "invoke":
        pending[op["process"]] = op
        continue
    inv = pending.pop(op["process"])
    micro = (op.get("txn") or inv["txn"])[0]
    if op["type"] == "ok" and micro["f"] == "append":
        appended.add(micro["key"])
    if op["type"] == "ok" and micro["f"] == "r" and op["process"] >= final_proc:
        final_ok.add(micro["key"])
missing = appended - final_ok
if missing:
    sys.exit(f"{len(missing)} keys were never read back after the test: {sorted(missing)[:5]}")
stats = report["check"]["stats"]
print(f"{outcome}: {stats['ok']} ok / {stats['fail']} fail / {stats['info']} info; "
      f"{len(appended)} keys verified by final reads; anomalies: {sorted(kinds) or 'none'}")
