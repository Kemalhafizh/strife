// Runs the Strife engine (WebAssembly) off the main thread.
//
// Messages in:  {type: "init", wasm, wasmGz, wasmExec}
//               {type: "call", id, op, args}
// Messages out: {type: "progress", loaded, total}
//               {type: "ready", version} | {type: "failed", error}
//               {type: "result", id, out}   (out is the JSON envelope string)
"use strict";

async function download(url) {
  const resp = await fetch(url);
  if (!resp.ok) throw new Error(`${url}: HTTP ${resp.status}`);
  const total = Number(resp.headers.get("Content-Length")) || 0;
  if (!resp.body || !resp.body.getReader) {
    return new Uint8Array(await resp.arrayBuffer());
  }
  const reader = resp.body.getReader();
  const chunks = [];
  let loaded = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    chunks.push(value);
    loaded += value.length;
    self.postMessage({ type: "progress", loaded, total });
  }
  const out = new Uint8Array(loaded);
  let off = 0;
  for (const c of chunks) { out.set(c, off); off += c.length; }
  return out;
}

async function gunzip(bytes) {
  const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream("gzip"));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}

async function init({ wasm, wasmGz, wasmExec }) {
  importScripts(wasmExec);
  // Prefer the gzip copy and inflate it here: the transfer is a quarter of
  // the size whether or not the host compresses .wasm responses. A host that
  // already decoded it hands us plain WebAssembly, which we keep as is.
  let bytes;
  if (typeof DecompressionStream === "function" && wasmGz) {
    bytes = await download(wasmGz);
    if (bytes[0] === 0x1f && bytes[1] === 0x8b) bytes = await gunzip(bytes);
  } else {
    bytes = await download(wasm);
  }
  const go = new self.Go();
  go.env = {};
  go.argv = ["strife"];
  const ready = new Promise((resolve) => { self.onStrifeReady = resolve; });
  const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
  go.run(instance); // resolves only when the program exits, which it does not
  await ready;
  return self.strife.version;
}

self.onmessage = async (event) => {
  const msg = event.data;
  if (msg.type === "init") {
    try {
      const version = await init(msg);
      self.postMessage({ type: "ready", version });
    } catch (err) {
      self.postMessage({ type: "failed", error: String(err && err.message || err) });
    }
    return;
  }
  if (msg.type === "call") {
    let out;
    try {
      const fn = self.strife && self.strife[msg.op];
      if (typeof fn !== "function") throw new Error(`unknown operation ${msg.op}`);
      out = fn(...msg.args);
    } catch (err) {
      out = JSON.stringify({ ok: false, error: String(err && err.message || err) });
    }
    self.postMessage({ type: "result", id: msg.id, out });
  }
};
