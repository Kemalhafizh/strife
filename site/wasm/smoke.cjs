// Loads the WebAssembly build the way the site's worker does and checks that
// every function answers and that each preset reproduces the hashes the
// native engine computed (data/presets.json).
//
//   node site/wasm/smoke.cjs <wasm_exec.js> <strife.wasm> <presets.json> <samples dir>
"use strict";

const fs = require("fs");
const path = require("path");

const [wasmExec, wasmFile, presetsFile, samples] = process.argv.slice(2);
if (!samples) {
  console.error("usage: node smoke.cjs <wasm_exec.js> <strife.wasm> <presets.json> <samples dir>");
  process.exit(2);
}

globalThis.require = require;
globalThis.fs = fs;
globalThis.path = path;
require(path.resolve(wasmExec));

let failures = 0;
function expect(ok, what) {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}`);
  if (!ok) failures++;
}

function call(name, ...args) {
  const out = JSON.parse(globalThis.strife[name](...args));
  return out;
}

(async () => {
  const go = new Go();
  go.env = {};
  const ready = new Promise((resolve) => { globalThis.onStrifeReady = resolve; });
  const { instance } = await WebAssembly.instantiate(fs.readFileSync(wasmFile), go.importObject);
  go.run(instance);
  await ready;

  const presets = JSON.parse(fs.readFileSync(presetsFile, "utf8"));
  expect(call("systems").result.length === presets.systems.length, "systems match the native listing");
  expect(call("faultKinds").result.join() === presets.faults.join(), "fault kinds match");

  for (const p of presets.presets) {
    const t = Date.now();
    const r = call("simulate", JSON.stringify({ system: p.system, faults: p.faults, seed: p.seed }));
    expect(r.ok && r.result.trace_hash === p.trace_hash && r.result.history_hash === p.history_hash && r.result.verdict === p.verdict,
      `${p.id}: ${r.ok ? r.result.verdict : r.error} in ${Date.now() - t} ms, same hashes as ${presets.platform}`);
  }

  const withReport = call("simulate", JSON.stringify({ system: "raft-stale-reads", faults: "partition", seed: 2, report: true }));
  expect(withReport.ok && withReport.result.report_html.includes("<html"), "report.html renders");
  expect(!call("simulate", JSON.stringify({ seed: 1, duration: "5m" })).ok, "over-long simulations are refused");

  const stale = call("check", fs.readFileSync(path.join(samples, "stale-read.jsonl")), JSON.stringify({ name: "stale-read.jsonl", report: true }));
  expect(stale.ok && stale.result.verdict === "violation" && /stale read/i.test(stale.result.failures[0].summary), "stale-read sample is a violation");
  const fresh = call("check", fs.readFileSync(path.join(samples, "fresh-read.jsonl")), JSON.stringify({ name: "fresh-read.jsonl" }));
  expect(fresh.ok && fresh.result.verdict === "linearizable", "fresh-read sample is linearizable");

  const bad = call("verifyAttestation", new TextEncoder().encode("{}"), new TextEncoder().encode("not a key"), null);
  expect(!bad.ok && /public key/.test(bad.error), "a bad key is reported, not thrown");
  const badRelease = call("verifyRelease", new TextEncoder().encode("nope"), new TextEncoder().encode("x"), []);
  expect(!badRelease.ok, "a bad release bundle is reported");

  console.log(failures ? `${failures} check(s) failed` : "all checks passed");
  process.exit(failures ? 1 : 0);
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
