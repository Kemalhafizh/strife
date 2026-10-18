package compliance

import (
	"fmt"
	"html"
	"io"
	"strings"
)

// HTML renders a self-contained, print-ready HTML5 compliance audit dossier.
func HTML(w io.Writer, d *Dossier) error {
	var b strings.Builder

	passCount, partialCount, failCount := 0, 0, 0
	for _, c := range d.Controls {
		switch c.Status {
		case StatusPass:
			passCount++
		case StatusPartial:
			partialCount++
		case StatusFail:
			failCount++
		}
	}

	b.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Strife Compliance Dossier — `)
	b.WriteString(html.EscapeString(d.Summary.Target))
	b.WriteString(`</title>
<style>
:root{
  --bg:#0b0f17;--panel:#111827;--panel2:#1f2937;--border:#2e3a4e;
  --fg:#f3f4f6;--muted:#9ca3af;--accent:#38bdf8;
  --ok:#22c55e;--ok-bg:rgba(34,197,94,0.12);
  --warn:#f59e0b;--warn-bg:rgba(245,158,11,0.12);
  --bad:#ef4444;--bad-bg:rgba(239,68,68,0.14);
  --mono:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,monospace;
  --sans:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;
}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font-family:var(--sans);line-height:1.5;padding:32px 20px}
.wrap{max-width:1180px;margin:0 auto}
header{display:flex;flex-wrap:wrap;justify-content:space-between;align-items:flex-start;gap:16px;padding-bottom:20px;border-bottom:1px solid var(--border)}
h1{margin:0 0 6px 0;font-size:24px;letter-spacing:-0.02em}
h2{margin:28px 0 12px 0;font-size:16px;text-transform:uppercase;letter-spacing:0.06em;color:var(--muted)}
.sub{color:var(--muted);font-size:13px;font-family:var(--mono)}
.cite{color:var(--muted);font-size:12px;margin-top:4px;font-style:italic}
.badge{display:inline-flex;align-items:center;gap:6px;padding:5px 12px;border-radius:999px;font-weight:700;font-size:12px;font-family:var(--mono);letter-spacing:0.04em}
.badge.pass{background:var(--ok-bg);color:var(--ok);border:1px solid rgba(34,197,94,0.35)}
.badge.partial{background:var(--warn-bg);color:var(--warn);border:1px solid rgba(245,158,11,0.35)}
.badge.fail{background:var(--bad-bg);color:var(--bad);border:1px solid rgba(239,68,68,0.35)}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:12px;margin-top:20px}
.card{background:var(--panel);border:1px solid var(--border);border-radius:10px;padding:14px 16px}
.card .k{font-size:11px;text-transform:uppercase;letter-spacing:0.06em;color:var(--muted)}
.card .v{font-size:22px;font-weight:700;margin-top:4px;font-family:var(--mono)}
.card .s{font-size:12px;color:var(--muted);margin-top:4px}
.filters{display:flex;flex-wrap:wrap;gap:8px;margin:14px 0}
.pill{background:var(--panel);color:var(--fg);border:1px solid var(--border);border-radius:999px;padding:5px 12px;font-size:12px;cursor:pointer;font-family:var(--mono)}
.pill.active{background:var(--accent);color:#06131f;border-color:var(--accent);font-weight:700}
table{width:100%;border-collapse:collapse;background:var(--panel);border:1px solid var(--border);border-radius:10px;overflow:hidden;font-size:13px}
th,td{text-align:left;padding:11px 14px;border-bottom:1px solid var(--border);vertical-align:top}
th{background:var(--panel2);color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:0.06em}
tr:last-child td{border-bottom:none}
code{font-family:var(--mono);font-size:12px;background:rgba(255,255,255,0.06);padding:2px 6px;border-radius:4px}
.gap{margin-top:6px;padding:6px 10px;border-radius:6px;background:rgba(245,158,11,0.08);border-left:3px solid var(--warn);color:#fcd34d;font-size:12px}
.cols{display:grid;grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:14px}
.box{background:var(--panel);border:1px solid var(--border);border-radius:10px;padding:16px}
.box h3{margin:0 0 8px 0;font-size:14px;color:var(--accent)}
.box ul{margin:0;padding-left:18px;font-size:13px;color:var(--fg)}
.box li{margin-bottom:6px}
.notice{margin-top:20px;padding:14px 16px;border-radius:10px;background:rgba(56,189,248,0.08);border:1px solid rgba(56,189,248,0.3);font-size:13px}
footer{margin-top:28px;padding-top:14px;border-top:1px solid var(--border);color:var(--muted);font-size:12px;font-family:var(--mono);display:flex;flex-wrap:wrap;gap:6px 16px;justify-content:space-between;overflow-wrap:anywhere}
.table-wrap{overflow-x:auto;-webkit-overflow-scrolling:touch}
@media (max-width:600px){body{padding:20px 16px}}
@media print{
  :root{--bg:#ffffff;--panel:#ffffff;--panel2:#f3f4f6;--border:#d1d5db;--fg:#111827;--muted:#4b5563}
  body{padding:0}
  .filters{display:none}
}
</style>
</head>
<body>
<div class="wrap">
<header>
  <div>
    <h1>Regulatory Compliance &amp; Resilience Dossier</h1>
    <div class="sub">Target: `)
	b.WriteString(html.EscapeString(d.Summary.Target))
	b.WriteString(` · Kind: `)
	b.WriteString(html.EscapeString(d.Summary.RunKind))
	b.WriteString(` · Model: `)
	b.WriteString(html.EscapeString(d.Summary.Model))
	b.WriteString(` · Dossier SHA-256: `)
	b.WriteString(html.EscapeString(shortHash(d.DossierHash)))
	b.WriteString(`</div>
  </div>
  <span class="badge `)
	b.WriteString(string(d.Overall))
	b.WriteString(`">OVERALL: `)
	b.WriteString(strings.ToUpper(string(d.Overall)))
	b.WriteString(`</span>
</header>

<div class="grid">
  <div class="card">
    <div class="k">Consistency Verdict</div>
    <div class="v">`)
	b.WriteString(html.EscapeString(strings.ToUpper(string(d.Summary.Verdict))))
	b.WriteString(`</div>
    <div class="s">`)
	fmt.Fprintf(&b, "%d / %d seed(s) linearizable", d.Summary.SeedsLinearizable, d.Summary.SeedsTested)
	b.WriteString(`</div>
  </div>
  <div class="card">
    <div class="k">Operations Verified</div>
    <div class="v">`)
	fmt.Fprintf(&b, "%d", d.Summary.TotalOperations)
	b.WriteString(`</div>
    <div class="s">`)
	fmt.Fprintf(&b, "%d cluster node(s)", d.Summary.Nodes)
	b.WriteString(`</div>
  </div>
  <div class="card">
    <div class="k">Controls Evaluated</div>
    <div class="v">`)
	fmt.Fprintf(&b, "%d", len(d.Controls))
	b.WriteString(`</div>
    <div class="s">`)
	fmt.Fprintf(&b, "%d pass · %d partial · %d fail", passCount, partialCount, failCount)
	b.WriteString(`</div>
  </div>
  <div class="card">
    <div class="k">Cryptographic Attestation</div>
    <div class="v">`)
	if d.Summary.Attested {
		b.WriteString("SIGNED (Ed25519)")
	} else {
		b.WriteString("UNSIGNED")
	}
	b.WriteString(`</div>
    <div class="s">`)
	if d.Summary.Attested {
		fmt.Fprintf(&b, "Signer: %s (%s)", html.EscapeString(d.Summary.Signer), html.EscapeString(d.Summary.KeyID))
	} else {
		fmt.Fprintf(&b, "Fingerprint: %s", html.EscapeString(shortHash(d.Summary.Fingerprint)))
	}
	b.WriteString(`</div>
  </div>
</div>

<h2>Regulatory Control Matrix</h2>
<div class="filters" id="fw-filters">
  <button class="pill active" data-fw="all" onclick="filterControls('all')">All Frameworks</button>`)
	for _, fw := range d.Frameworks {
		fmt.Fprintf(&b, `<button class="pill" data-fw="%s" onclick="filterControls('%s')">%s</button>`,
			html.EscapeString(string(fw)), html.EscapeString(string(fw)), html.EscapeString(strings.ToUpper(string(fw))))
	}
	b.WriteString(`
</div>
<div class="table-wrap">
<table id="ctrl-table">
  <thead>
    <tr>
      <th>Framework</th>
      <th>Control ID</th>
      <th>Requirement</th>
      <th>Status</th>
      <th>Empirical Evidence &amp; Remediation</th>
    </tr>
  </thead>
  <tbody>`)
	for _, c := range d.Controls {
		fmt.Fprintf(&b, `<tr data-fw="%s">`, html.EscapeString(string(c.Framework)))
		fmt.Fprintf(&b, `<td><code>%s</code></td>`, html.EscapeString(strings.ToUpper(string(c.Framework))))
		fmt.Fprintf(&b, `<td><strong>%s</strong><div class="sub">%s</div></td>`,
			html.EscapeString(c.ControlID), html.EscapeString(c.Title))
		fmt.Fprintf(&b, `<td>%s<div class="cite">%s</div></td>`, html.EscapeString(c.Requirement), html.EscapeString(c.Citation))
		fmt.Fprintf(&b, `<td><span class="badge %s">%s</span></td>`,
			html.EscapeString(string(c.Status)), html.EscapeString(strings.ToUpper(string(c.Status))))
		b.WriteString(`<td>`)
		b.WriteString(html.EscapeString(c.Evidence))
		if c.Gap != "" {
			fmt.Fprintf(&b, `<div class="gap"><strong>Gap / Action:</strong> %s</div>`, html.EscapeString(c.Gap))
		}
		b.WriteString(`</td></tr>`)
	}
	b.WriteString(`</tbody>
</table>
</div>

<h2>Methodology, Scope &amp; Limitations</h2>
<div class="cols">
  <div class="box">
    <h3>What Is Tested</h3>
    <ul>`)
	for _, item := range d.Methodology.WhatIsTested {
		fmt.Fprintf(&b, `<li>%s</li>`, html.EscapeString(item))
	}
	b.WriteString(`</ul>
  </div>
  <div class="box">
    <h3>What Is Not Tested</h3>
    <ul>`)
	for _, item := range d.Methodology.WhatIsNotTested {
		fmt.Fprintf(&b, `<li>%s</li>`, html.EscapeString(item))
	}
	b.WriteString(`</ul>
  </div>
  <div class="box">
    <h3>Execution &amp; Reproducibility Boundaries</h3>
    <ul>`)
	for _, item := range d.Methodology.ExecutionLimits {
		fmt.Fprintf(&b, `<li>%s</li>`, html.EscapeString(item))
	}
	b.WriteString(`</ul>
  </div>
</div>

<div class="notice">
  <strong>Auditor Disclaimer:</strong> `)
	b.WriteString(html.EscapeString(d.Methodology.AuditorDisclaimer))
	b.WriteString(`
</div>

<footer>
  <span>Strife `)
	b.WriteString(html.EscapeString(d.Version))
	b.WriteString(` · Tamper-evident fingerprint: `)
	b.WriteString(html.EscapeString(d.Summary.Fingerprint))
	b.WriteString(`</span>
  <span>Generated `)
	b.WriteString(html.EscapeString(d.GeneratedAt))
	b.WriteString(`</span>
</footer>
</div>
<script>
function filterControls(fw){
  document.querySelectorAll('#fw-filters .pill').forEach(function(btn){
    btn.classList.toggle('active', btn.getAttribute('data-fw')===fw);
  });
  document.querySelectorAll('#ctrl-table tbody tr').forEach(function(row){
    row.style.display = (fw==='all' || row.getAttribute('data-fw')===fw) ? '' : 'none';
  });
}
</script>
</body>
</html>
`)

	_, err := io.WriteString(w, b.String())
	return err
}
