// The verifier: `strife verify` for attestation bundles and release files,
// in the browser.
(() => {
  "use strict";
  const { Engine, t, h, verdictBadge, bindStatus, dropZone, readFile } = window.Strife;
  const root = document.getElementById("verify");
  if (!root) return;

  const result = document.getElementById("result");
  const officialKey = root.dataset.releaseKey ? new TextEncoder().encode(root.dataset.releaseKey) : null;
  const files = {}; // slot id -> [{name, data}]
  const forms = {
    attest: document.getElementById("pane-attest"),
    release: document.getElementById("pane-release"),
  };

  const s = bindStatus(document.getElementById("status"), t("try_loading"));
  const engine = new Engine(root, {
    onProgress: s.onProgress,
    onReady: (v) => { s.onReady(v); update(); },
    onFailed: s.onFailed,
  });

  // Tabs
  const tabs = [...document.querySelectorAll("[role=tab]")];
  function select(tab) {
    for (const x of tabs) {
      const on = x === tab;
      x.setAttribute("aria-selected", String(on));
      x.tabIndex = on ? 0 : -1;
      document.getElementById(x.getAttribute("aria-controls")).hidden = !on;
    }
  }
  tabs.forEach((tab, i) => {
    tab.addEventListener("click", () => select(tab));
    tab.addEventListener("keydown", (e) => {
      const d = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
      if (!d) return;
      const next = tabs[(i + d + tabs.length) % tabs.length];
      select(next);
      next.focus();
    });
  });

  for (const zone of document.querySelectorAll("[data-slot]")) {
    const slot = zone.dataset.slot;
    dropZone(zone, async (picked) => {
      files[slot] = await Promise.all(picked.map(async (f) => ({ name: f.name, data: await readFile(f) })));
      zone.querySelector("[data-name]").textContent = files[slot].map((f) => f.name).join(", ");
      update();
    });
  }

  const first = (slot) => (files[slot] && files[slot][0] ? files[slot][0].data : null);
  const releaseKey = () => first("r-key") || officialKey;

  function update() {
    const ready = !!engine.version;
    forms.attest.querySelector("[type=submit]").disabled = !(ready && first("a-bundle") && first("a-key"));
    forms.release.querySelector("[type=submit]").disabled = !(ready && first("r-bundle") && releaseKey());
  }

  // A real bundle, key and history, produced by the CLI when the site was built.
  const sample = document.getElementById("load-sample");
  if (sample) {
    sample.addEventListener("click", async () => {
      const slots = { "a-bundle": "bundle.json", "a-key": "example-auditor.pub", "a-history": "history.jsonl" };
      try {
        for (const [slot, name] of Object.entries(slots)) {
          const resp = await fetch(sample.dataset.base + name);
          if (!resp.ok) throw new Error(`${name}: HTTP ${resp.status}`);
          files[slot] = [{ name, data: new Uint8Array(await resp.arrayBuffer()) }];
          document.querySelector(`[data-slot="${slot}"] [data-name]`).textContent = name;
        }
        await engine.ready;
        update();
        forms.attest.requestSubmit();
      } catch (err) {
        fail(err);
      }
    });
  }

  forms.attest.addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
      const res = await engine.call("verifyAttestation", first("a-bundle"), first("a-key"), first("a-history"));
      renderAttestation(res);
    } catch (err) {
      fail(err);
    }
  });

  forms.release.addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
      const res = await engine.call("verifyRelease", first("r-bundle"), releaseKey(), files["r-files"] || []);
      renderRelease(res);
    } catch (err) {
      fail(err);
    }
  });

  function fail(err) {
    result.replaceChildren(h("div", { class: "empty", role: "alert" }, String(err.message || err)));
  }

  function head(valid, problem) {
    return [
      h("div", { class: "result-head" },
        h("span", { class: `badge ${valid ? "badge-ok" : "badge-violation"}` }, valid ? t("verify_valid") : t("verify_invalid"))),
      problem ? h("p", { class: "check bad" }, problem) : null,
    ];
  }

  function kv(rows) {
    return h("dl", { class: "kv" }, rows.filter(([, v]) => v !== undefined && v !== null && v !== "")
      .flatMap(([k, v]) => [h("dt", {}, k), h("dd", {}, v)]));
  }

  function renderAttestation(r) {
    const out = head(r.valid, r.problem);
    if (r.history_checked) {
      out.push(h("p", { class: `check${r.history_matches ? "" : " bad"}` }, r.history_matches ? t("verify_history_ok") : t("verify_history_bad")));
    }
    out.push(kv([
      [t("verify_key_id"), r.key_id],
      [t("verify_bundle_keys"), (r.bundle_keys || []).join(", ")],
      [t("verify_signer"), r.signer],
      [t("verify_signed_at"), r.signed_at],
      [t("verify_target"), r.target ? `${r.target}${r.seed ? " · seed " + r.seed : ""}` : ""],
      [t("verify_verdict"), r.verdict ? verdictBadge(r.verdict) : ""],
      [t("verify_fingerprint"), r.fingerprint],
      ["history", r.history_hash],
    ]));
    result.replaceChildren(...out.filter(Boolean));
  }

  function renderRelease(r) {
    const out = head(r.valid, r.problem);
    out.push(kv([
      [t("verify_key_id"), r.key_id],
      [t("verify_bundle_keys"), (r.bundle_keys || []).join(", ")],
      [t("verify_version"), r.version],
      [t("verify_commit"), r.commit],
      [t("verify_builder"), r.builder],
      [t("verify_signer"), r.signer],
      [t("verify_signed_at"), r.signed_at],
    ]));
    if (r.files && r.files.length) {
      const label = { match: t("verify_file_match"), mismatch: t("verify_file_mismatch"), "not-in-release": t("verify_file_unknown") };
      out.push(h("ul", { class: "files" }, r.files.map((f) =>
        h("li", {},
          h("span", { class: `check${f.status === "match" ? "" : " bad"}` }, `${f.name}: ${label[f.status] || f.status}`),
          h("code", {}, `sha256 ${f.sha256}`)))));
    }
    result.replaceChildren(...out.filter(Boolean));
  }
})();
