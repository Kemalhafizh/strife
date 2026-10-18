// The history studio: runs `strife check` on a history.jsonl in the browser.
(() => {
  "use strict";
  const { Engine, t, h, verdictBadge, downloadText, reportFrame, bindStatus, dropZone, readFile } = window.Strife;
  const root = document.getElementById("studio");
  if (!root) return;

  const form = document.getElementById("studio-form");
  const checkBtn = document.getElementById("check");
  const cancelBtn = document.getElementById("cancel");
  const result = document.getElementById("result");
  const fileName = document.getElementById("file-name");
  const model = document.getElementById("model");
  const isolation = document.getElementById("isolation");
  const nf = new Intl.NumberFormat(document.documentElement.lang || undefined);
  let file = null; // {name, data}
  let busy = false;

  const s = bindStatus(document.getElementById("status"), t("try_loading"));
  const engine = new Engine(root, {
    onProgress: s.onProgress,
    onReady: (v) => { s.onReady(v); update(); },
    onFailed: s.onFailed,
  });

  function update() {
    checkBtn.disabled = busy || !file || !engine.version;
    isolation.disabled = !["txn", "append"].includes(model.value);
  }
  model.addEventListener("change", update);
  update();

  dropZone(document.getElementById("drop"), async ([f]) => {
    file = { name: f.name, data: await readFile(f) };
    fileName.textContent = `${f.name} (${Strife.formatBytes(f.size) || f.size + " B"})`;
    update();
  });

  for (const a of document.querySelectorAll("[data-sample]")) {
    a.addEventListener("click", async (e) => {
      e.preventDefault();
      const resp = await fetch(a.href);
      const name = a.href.split("/").pop();
      file = { name, data: new Uint8Array(await resp.arrayBuffer()) };
      fileName.textContent = name;
      model.value = "kv";
      update();
      if (engine.version) check();
    });
  }

  async function check() {
    if (!file || busy) return;
    busy = true;
    update();
    checkBtn.textContent = t("studio_checking");
    cancelBtn.hidden = false;
    const started = performance.now();
    try {
      const opts = { name: file.name, model: model.value, isolation: isolation.value, report: true };
      const res = await engine.call("check", file.data, JSON.stringify(opts));
      render(res, Math.round(performance.now() - started));
    } catch (err) {
      if (err.name !== "AbortError") result.replaceChildren(h("div", { class: "empty", role: "alert" }, String(err.message || err)));
    } finally {
      busy = false;
      checkBtn.textContent = t("studio_check");
      cancelBtn.hidden = true;
      update();
    }
  }
  form.addEventListener("submit", (e) => { e.preventDefault(); check(); });
  cancelBtn.addEventListener("click", () => engine.cancel());

  function render(res, ms) {
    const ops = res.ops || {};
    const out = [
      h("div", { class: "result-head" },
        verdictBadge(res.verdict),
        h("span", { class: "muted small" },
          `${res.model} · ${nf.format(res.keys)} ${t("studio_keys")} · ${nf.format(ops.invocations || 0)} ${t("try_ops")} ` +
          `(${nf.format(ops.ok || 0)} ${t("try_ok")}, ${nf.format(ops.fail || 0)} ${t("try_fail")}, ${nf.format(ops.info || 0)} ${t("try_info")}) · ${nf.format(ms)} ms ${t("try_elapsed")}`)),
    ];
    if (res.failures && res.failures.length) {
      out.push(h("h3", {}, t("try_explain")));
      out.push(h("div", { class: "explain" }, res.failures.slice(0, 6).map((f) => `key ${f.key}: ${f.summary}`).join("\n\n")));
    }
    if (res.findings && res.findings.length) {
      out.push(h("h3", {}, t("studio_findings")));
      out.push(h("div", { class: "explain" }, res.findings.join("\n\n")));
    }
    out.push(h("dl", { class: "kv" },
      h("dt", {}, "history"), h("dd", {}, res.history_hash),
      h("dt", {}, t("verify_fingerprint")), h("dd", {}, res.fingerprint)));
    const dl = h("button", { type: "button", class: "btn btn-ghost btn-sm" }, t("try_download_report"));
    dl.addEventListener("click", () => downloadText("report.html", res.report_html));
    out.push(h("div", { class: "actions" }, dl));
    if (res.report_html) {
      out.push(h("h3", {}, t("try_report")));
      out.push(reportFrame(res.report_html, `report ${file.name}`));
    }
    result.replaceChildren(...out);
  }
})();
