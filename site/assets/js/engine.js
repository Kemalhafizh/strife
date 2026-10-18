// A small client for the engine worker, shared by the playground, the
// studio and the verifier. The page's tool element carries the asset URLs:
//   data-worker, data-wasm, data-wasm-gz, data-wasm-exec
(() => {
  "use strict";

  class Engine {
    constructor(el, { onProgress, onReady, onFailed } = {}) {
      this.urls = {
        worker: el.dataset.worker,
        wasm: el.dataset.wasm,
        wasmGz: el.dataset.wasmGz,
        wasmExec: el.dataset.wasmExec,
      };
      this.handlers = { onProgress, onReady, onFailed };
      this.pending = new Map();
      this.seq = 0;
      this.ready = null;
      this.start();
    }

    start() {
      this.worker = new Worker(this.urls.worker);
      this.ready = new Promise((resolve, reject) => {
        this.worker.onmessage = (e) => {
          const m = e.data;
          if (m.type === "progress") {
            this.handlers.onProgress && this.handlers.onProgress(m.loaded, m.total);
          } else if (m.type === "ready") {
            this.version = m.version;
            this.handlers.onReady && this.handlers.onReady(m.version);
            resolve(m.version);
          } else if (m.type === "failed") {
            this.handlers.onFailed && this.handlers.onFailed(m.error);
            reject(new Error(m.error));
          } else if (m.type === "result") {
            const p = this.pending.get(m.id);
            if (!p) return;
            this.pending.delete(m.id);
            let env;
            try { env = JSON.parse(m.out); } catch (err) { p.reject(err); return; }
            if (env.ok) p.resolve(env.result); else p.reject(new Error(env.error));
          }
        };
        this.worker.onerror = (e) => {
          const msg = e.message || "worker error";
          this.handlers.onFailed && this.handlers.onFailed(msg);
          reject(new Error(msg));
        };
      });
      this.ready.catch(() => {});
      this.worker.postMessage({ type: "init", ...this.urls });
    }

    async call(op, ...args) {
      await this.ready;
      const id = ++this.seq;
      return new Promise((resolve, reject) => {
        this.pending.set(id, { resolve, reject });
        this.worker.postMessage({ type: "call", id, op, args });
      });
    }

    // Stops a long computation by restarting the worker.
    cancel() {
      this.worker.terminate();
      for (const p of this.pending.values()) p.reject(new DOMException("cancelled", "AbortError"));
      this.pending.clear();
      this.start();
    }
  }

  // Helpers shared by the tool pages.
  const strings = (() => {
    const el = document.getElementById("strings");
    try { return el ? JSON.parse(el.textContent) : {}; } catch { return {}; }
  })();

  function t(key, ...args) {
    let s = strings[key] || key;
    for (const a of args) s = s.replace("%s", a);
    return s;
  }

  function h(tag, attrs = {}, ...children) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v === null || v === undefined || v === false) continue;
      if (k === "class") el.className = v;
      else if (k === "text") el.textContent = v;
      else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
      else el.setAttribute(k, v === true ? "" : v);
    }
    for (const c of children.flat()) {
      if (c === null || c === undefined || c === false) continue;
      el.append(c instanceof Node ? c : document.createTextNode(String(c)));
    }
    return el;
  }

  function verdictBadge(verdict) {
    const v = ["violation", "linearizable"].includes(verdict) ? verdict : "unknown";
    return h("span", { class: `badge badge-${v}` }, t(`verdict_${v}`));
  }

  function formatBytes(n) {
    if (!n) return "";
    return n > 1048576 ? `${(n / 1048576).toFixed(1)} MB` : `${Math.round(n / 1024)} KB`;
  }

  function readFile(file) {
    return file.arrayBuffer().then((b) => new Uint8Array(b));
  }

  function downloadText(name, text, type = "text/html") {
    const url = URL.createObjectURL(new Blob([text], { type }));
    const a = h("a", { href: url, download: name });
    document.body.append(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 10000);
  }

  // A sandboxed frame for an engine-rendered report: scripts run, but in an
  // opaque origin, away from this page.
  function reportFrame(html, title) {
    return h("iframe", { class: "report-frame", sandbox: "allow-scripts allow-popups", title, srcdoc: html, loading: "lazy" });
  }

  // Shows load progress in a .status element until the engine is ready.
  function bindStatus(statusEl, label) {
    const bar = statusEl.querySelector(".progress span");
    const text = statusEl.querySelector("[data-status-text]");
    return {
      onProgress(loaded, total) {
        if (bar && total) bar.style.width = `${Math.min(100, (100 * loaded) / total).toFixed(0)}%`;
        if (text) text.textContent = `${label}… ${formatBytes(loaded)}${total ? " / " + formatBytes(total) : ""}`;
      },
      onReady() {
        statusEl.querySelector(".spinner")?.remove();
        statusEl.querySelector(".progress")?.remove();
        if (text) text.textContent = t("try_ready");
        statusEl.classList.add("is-ready");
      },
      onFailed(err) {
        statusEl.querySelector(".spinner")?.remove();
        if (text) text.textContent = `${t("try_load_failed")} (${err})`;
        statusEl.classList.add("is-failed");
      },
    };
  }

  function dropZone(zone, onFiles) {
    const input = zone.querySelector("input[type=file]");
    zone.addEventListener("dragover", (e) => { e.preventDefault(); zone.classList.add("is-over"); });
    zone.addEventListener("dragleave", () => zone.classList.remove("is-over"));
    zone.addEventListener("drop", (e) => {
      e.preventDefault();
      zone.classList.remove("is-over");
      if (e.dataTransfer.files.length) onFiles([...e.dataTransfer.files]);
    });
    input.addEventListener("change", () => { if (input.files.length) onFiles([...input.files]); });
    zone.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") { e.preventDefault(); input.click(); }
    });
  }

  window.Strife = { Engine, t, h, verdictBadge, formatBytes, readFile, downloadText, reportFrame, bindStatus, dropZone, strings };
})();
