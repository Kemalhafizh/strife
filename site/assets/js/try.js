// The simulation playground: runs `strife sim` in the browser.
(() => {
  "use strict";
  const { Engine, t, h, verdictBadge, downloadText, reportFrame, bindStatus } = window.Strife;
  const root = document.getElementById("try");
  if (!root) return;

  const presets = JSON.parse(document.getElementById("presets").textContent);
  const form = document.getElementById("try-form");
  const runBtn = document.getElementById("run");
  const cancelBtn = document.getElementById("cancel");
  const status = document.getElementById("status");
  const result = document.getElementById("result");
  const sysDesc = document.getElementById("f-system-desc");
  const chips = [...document.querySelectorAll("#f-faults input")];
  const allKinds = chips.map((c) => c.value);
  const presetButtons = [...document.querySelectorAll("[data-preset]")];
  const DEFAULTS = { nodes: 5, clients: 10, duration: 10 };
  const nf = new Intl.NumberFormat(document.documentElement.lang || undefined);

  // The engine gets faults in this order, and the order changes the
  // schedule, so it is kept exactly as chosen (or as a preset gives it).
  let faultOrder = allKinds.slice();
  let running = false;
  let previous = null;

  const s = bindStatus(status, t("try_loading"));
  const engine = new Engine(root, {
    onProgress: s.onProgress,
    onReady: (v) => { s.onReady(v); runBtn.disabled = false; maybeAutorun(); },
    onFailed: s.onFailed,
  });

  function showSystem() {
    const opt = form.elements.system.selectedOptions[0];
    sysDesc.textContent = opt ? opt.dataset.desc : "";
  }
  form.elements.system.addEventListener("change", () => { showSystem(); clearPreset(); });
  showSystem();

  function setFaults(spec) {
    faultOrder = spec === "all" ? allKinds.slice() : spec === "none" || spec === "" ? [] : spec.split(",");
    for (const c of chips) c.checked = faultOrder.includes(c.value);
  }
  for (const c of chips) {
    c.addEventListener("change", () => {
      if (c.checked && !faultOrder.includes(c.value)) faultOrder.push(c.value);
      if (!c.checked) faultOrder = faultOrder.filter((k) => k !== c.value);
      clearPreset();
    });
  }
  for (const name of ["seed", "duration", "nodes", "clients"]) {
    form.elements[name].addEventListener("input", clearPreset);
  }

  function faultSpec() {
    if (faultOrder.length === 0) return "none";
    if (faultOrder.length === allKinds.length && faultOrder.every((k, i) => k === allKinds[i])) return "all";
    return faultOrder.join(",");
  }

  function clearPreset() {
    for (const b of presetButtons) b.setAttribute("aria-pressed", "false");
  }

  function applyPreset(id) {
    const p = presets.find((x) => x.id === id);
    if (!p) return null;
    form.elements.system.value = p.system;
    showSystem();
    form.elements.seed.value = String(p.seed);
    form.elements.duration.value = DEFAULTS.duration;
    form.elements.nodes.value = DEFAULTS.nodes;
    form.elements.clients.value = DEFAULTS.clients;
    setFaults(p.faults);
    for (const b of presetButtons) b.setAttribute("aria-pressed", String(b.dataset.preset === id));
    return p;
  }

  for (const b of presetButtons) {
    b.addEventListener("click", () => {
      applyPreset(b.dataset.preset);
      if (!runBtn.disabled) run();
    });
  }

  function request() {
    const seed = form.elements.seed.value.trim();
    if (!/^\d+$/.test(seed) || Number(seed) > Number.MAX_SAFE_INTEGER) {
      form.elements.seed.setCustomValidity("0 – 9007199254740991");
      form.elements.seed.reportValidity();
      return null;
    }
    form.elements.seed.setCustomValidity("");
    for (const name of ["duration", "nodes", "clients"]) {
      if (!form.elements[name].reportValidity()) return null;
    }
    return {
      system: form.elements.system.value,
      faults: faultSpec(),
      seed: Number(seed),
      nodes: Number(form.elements.nodes.value),
      clients: Number(form.elements.clients.value),
      duration: `${Number(form.elements.duration.value)}s`,
      report: true,
    };
  }

  // The preset this request reproduces exactly, if any.
  function presetFor(req) {
    if (req.nodes !== DEFAULTS.nodes || req.clients !== DEFAULTS.clients || req.duration !== `${DEFAULTS.duration}s`) return null;
    return presets.find((p) => p.system === req.system && p.faults === req.faults && p.seed === req.seed) || null;
  }

  async function run(again = false) {
    const req = again && previous ? previous.req : request();
    if (!req || running) return;
    running = true;
    runBtn.disabled = true;
    runBtn.textContent = t("try_running");
    cancelBtn.hidden = false;
    result.setAttribute("aria-busy", "true");
    const started = performance.now();
    try {
      const res = await engine.call("simulate", JSON.stringify(req));
      const ms = Math.round(performance.now() - started);
      const same = again && previous ? previous.res.trace_hash === res.trace_hash && previous.res.history_hash === res.history_hash : null;
      render(req, res, ms, same);
      previous = { req, res };
    } catch (err) {
      if (err.name !== "AbortError") {
        result.replaceChildren(h("div", { class: "empty", role: "alert" }, String(err.message || err)));
      }
    } finally {
      running = false;
      runBtn.disabled = false;
      runBtn.textContent = t("try_run");
      cancelBtn.hidden = true;
      result.removeAttribute("aria-busy");
    }
  }

  cancelBtn.addEventListener("click", () => engine.cancel());
  form.addEventListener("submit", (e) => { e.preventDefault(); run(); });

  function copyButton(text) {
    const b = h("button", { type: "button", class: "copy-btn" }, t("copy"));
    b.addEventListener("click", () => window.StrifeSite.copyText(text, b));
    return b;
  }

  function render(req, res, ms, same) {
    const ops = res.ops || {};
    const preset = presetFor(req);
    const out = [];
    out.push(h("div", { class: "result-head" },
      verdictBadge(res.verdict),
      h("span", { class: "muted small" },
        `${nf.format(res.events)} ${t("try_events")} · ${nf.format(ops.invocations || 0)} ${t("try_ops")} ` +
        `(${nf.format(ops.ok || 0)} ${t("try_ok")}, ${nf.format(ops.fail || 0)} ${t("try_fail")}, ${nf.format(ops.info || 0)} ${t("try_info")}) · ${nf.format(ms)} ms ${t("try_elapsed")}`)));

    if (res.failures && res.failures.length) {
      out.push(h("h3", {}, t("try_explain")));
      out.push(h("div", { class: "explain" }, res.failures.slice(0, 4).map((f) => `key ${f.key}: ${f.summary}`).join("\n\n")));
    }

    out.push(h("h3", {}, t("try_schedule")));
    if (res.faults.length) {
      out.push(h("ul", { class: "schedule" }, res.faults.map((f) =>
        h("li", {},
          h("span", {}, `${f.start} → ${f.end}`),
          h("span", { class: `k${["kill", "pause", "freeze"].includes(f.kind) ? " red" : ""}` }, f.kind),
          h("span", {}, `[${f.nodes.join(" ")}]`)))));
    } else {
      out.push(h("p", { class: "muted small" }, "–"));
    }

    out.push(h("h3", {}, t("try_hashes")));
    out.push(h("dl", { class: "kv" },
      h("dt", {}, "trace"), h("dd", {}, res.trace_hash),
      h("dt", {}, "history"), h("dd", {}, res.history_hash),
      h("dt", {}, "schedule"), h("dd", {}, res.schedule_hash)));
    if (preset) {
      const ok = preset.trace_hash === res.trace_hash && preset.history_hash === res.history_hash;
      out.push(h("p", { class: `check${ok ? "" : " bad"}` }, `${ok ? t("try_match") : t("try_nomatch")} (${root.dataset.platform})`));
    }
    if (same !== null) {
      out.push(h("p", { class: `check${same ? "" : " bad"}` }, same ? t("try_same_again") : t("try_diff_again")));
    }

    out.push(h("h3", {}, t("try_command")));
    out.push(h("div", { class: "code-line" }, h("code", {}, res.command), copyButton(res.command)));

    const again = h("button", { type: "button", class: "btn btn-secondary btn-sm" }, t("try_again"));
    again.addEventListener("click", () => run(true));
    const dl = h("button", { type: "button", class: "btn btn-ghost btn-sm" }, t("try_download_report"));
    dl.addEventListener("click", () => downloadText(`report-${res.system}-seed${res.seed}.html`, res.report_html));
    out.push(h("div", { class: "actions" }, again, dl));

    if (res.report_html) {
      out.push(h("h3", {}, t("try_report")));
      out.push(h("p", { class: "muted small" }, t("try_report_hint")));
      out.push(reportFrame(res.report_html, `report ${res.system} seed ${res.seed}`));
    }
    result.replaceChildren(...out);
  }

  let autorun = new URLSearchParams(location.search).get("preset");
  if (autorun) applyPreset(autorun);
  function maybeAutorun() {
    if (autorun && presets.some((p) => p.id === autorun)) {
      autorun = null;
      run();
    }
  }
})();
