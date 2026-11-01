package serve

import (
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/report"
)

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	targetFilter := strings.TrimSpace(r.URL.Query().Get("target"))
	runs := s.store.List(targetFilter, "")
	targets := s.store.Targets()

	var totalRuns, passRuns, violationRuns, totalRegressions, signedCount int
	for _, e := range runs {
		totalRuns++
		if e.Verdict == checker.Linearizable {
			passRuns++
		} else if e.Verdict == checker.Violation {
			violationRuns++
		}
		totalRegressions += e.Regressions
		if e.Signed {
			signedCount++
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Strife Run Registry & Continuous Verification</title>
<style>
  :root {
    --bg: #0b0f17;
    --panel: #111827;
    --border: #1f2937;
    --text: #e5e7eb;
    --muted: #9ca3af;
    --accent: #38bdf8;
    --ok: #22c55e;
    --fail: #ef4444;
    --warn: #f59e0b;
    --mono: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    background: var(--bg);
    color: var(--text);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    line-height: 1.5;
  }
  header {
    border-bottom: 1px solid var(--border);
    background: #0d131f;
    padding: 1.25rem 2rem;
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  header h1 { margin: 0; font-size: 1.15rem; font-weight: 600; letter-spacing: -0.01em; }
  header .sub { color: var(--muted); font-size: 0.85rem; font-family: var(--mono); }
  main { max-width: 1280px; margin: 0 auto; padding: 1.75rem 2rem; }
  .kpis {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
    gap: 1rem;
    margin-bottom: 1.75rem;
  }
  .card {
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 1rem 1.25rem;
  }
  .card .label { color: var(--muted); font-size: 0.78rem; text-transform: uppercase; letter-spacing: 0.04em; }
  .card .val { font-size: 1.65rem; font-weight: 700; font-family: var(--mono); margin-top: 0.25rem; }
  .badge {
    display: inline-block;
    padding: 0.15rem 0.55rem;
    border-radius: 999px;
    font-size: 0.75rem;
    font-weight: 600;
    font-family: var(--mono);
    text-transform: uppercase;
  }
  .badge-ok { background: rgba(34,197,94,0.15); color: var(--ok); border: 1px solid rgba(34,197,94,0.35); }
  .badge-fail { background: rgba(239,68,68,0.15); color: var(--fail); border: 1px solid rgba(239,68,68,0.35); }
  .badge-warn { background: rgba(245,158,11,0.15); color: var(--warn); border: 1px solid rgba(245,158,11,0.35); }
  .badge-info { background: rgba(56,189,248,0.14); color: var(--accent); border: 1px solid rgba(56,189,248,0.35); }
  table {
    width: 100%;
    border-collapse: collapse;
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
    font-size: 0.88rem;
  }
  th, td {
    text-align: left;
    padding: 0.75rem 1rem;
    border-bottom: 1px solid var(--border);
  }
  th { color: var(--muted); font-weight: 600; font-size: 0.76rem; text-transform: uppercase; background: #0f1624; }
  td.mono { font-family: var(--mono); font-size: 0.82rem; }
  a { color: var(--accent); text-decoration: none; }
  a:hover { text-decoration: underline; }
  .filters { margin-bottom: 1rem; display: flex; gap: 0.5rem; align-items: center; flex-wrap: wrap; }
  .pill {
    padding: 0.25rem 0.7rem;
    border-radius: 6px;
    border: 1px solid var(--border);
    background: var(--panel);
    color: var(--text);
    font-size: 0.82rem;
    font-family: var(--mono);
  }
  pre.cli {
    background: #080c14;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.9rem 1.1rem;
    font-family: var(--mono);
    font-size: 0.82rem;
    color: #cbd5e1;
    overflow-x: auto;
    margin-top: 1.5rem;
  }
</style>
</head>
<body>
<header>
  <div>
    <h1>Strife Continuous Verification &amp; Run Registry</h1>
    <div class="sub">strife `)
	b.WriteString(html.EscapeString(report.Version))
	b.WriteString(` · store: `)
	b.WriteString(html.EscapeString(s.store.Dir()))
	b.WriteString(`</div>
  </div>
  <div class="sub"><a href="/api/v1/runs">/api/v1/runs</a> · <a href="/api/v1/health">/api/v1/health</a></div>
</header>
<main>
  <div class="kpis">
    <div class="card"><div class="label">Stored Runs &amp; Campaigns</div><div class="val">`)
	fmt.Fprintf(&b, "%d", totalRuns)
	b.WriteString(`</div></div>
    <div class="card"><div class="label">Linearizable / Violating</div><div class="val"><span style="color:var(--ok)">`)
	fmt.Fprintf(&b, "%d", passRuns)
	b.WriteString(`</span> / <span style="color:var(--fail)">`)
	fmt.Fprintf(&b, "%d", violationRuns)
	b.WriteString(`</span></div></div>
    <div class="card"><div class="label">Seed Regressions Detected</div><div class="val">`)
	fmt.Fprintf(&b, "%d", totalRegressions)
	b.WriteString(`</div></div>
    <div class="card"><div class="label">Ed25519 Signed Artifacts</div><div class="val">`)
	fmt.Fprintf(&b, "%d", signedCount)
	b.WriteString(`</div></div>
  </div>`)

	if len(targets) > 0 {
		b.WriteString(`<div class="filters"><span style="color:var(--muted);font-size:0.82rem">Filter target:</span><a class="pill" href="/">all</a>`)
		for _, t := range targets {
			esc := html.EscapeString(t)
			fmt.Fprintf(&b, `<a class="pill" href="/?target=%s">%s</a>`, esc, esc)
		}
		b.WriteString(`</div>`)
	}

	b.WriteString(`
  <table>
    <thead>
      <tr>
        <th>Run ID</th>
        <th>Kind</th>
        <th>Target</th>
        <th>Model</th>
        <th>Verdict</th>
        <th>Seeds / Ops</th>
        <th>Baseline Delta</th>
        <th>Fingerprint</th>
        <th>Attestation</th>
        <th>Timestamp</th>
      </tr>
    </thead>
    <tbody>`)

	if len(runs) == 0 {
		b.WriteString(`<tr><td colspan="10" style="text-align:center;color:var(--muted);padding:2rem">No runs ingested yet. POST a report.json or campaign.json to <code>/api/v1/ingest</code>.</td></tr>`)
	}
	for _, e := range runs {
		verdictClass := "badge-warn"
		if e.Verdict == checker.Linearizable {
			verdictClass = "badge-ok"
		} else if e.Verdict == checker.Violation {
			verdictClass = "badge-fail"
		}
		deltaStr := "-"
		if e.Kind == "campaign" {
			deltaStr = fmt.Sprintf("+%d reg / +%d fix", e.Regressions, e.Fixes)
		}
		attStr := "unsigned"
		if e.Signed {
			attStr = fmt.Sprintf("ed25519 (%s)", html.EscapeString(shortFP(e.KeyID)))
		}
		fmt.Fprintf(&b, `<tr>
        <td class="mono"><a href="/api/v1/runs/%s">%s</a></td>
        <td class="mono">%s</td>
        <td class="mono">%s</td>
        <td class="mono">%s</td>
        <td><span class="badge %s">%s</span></td>
        <td class="mono">%d seeds · %d ops</td>
        <td class="mono">%s</td>
        <td class="mono">%s</td>
        <td class="mono">%s</td>
        <td class="mono">%s</td>
      </tr>`,
			html.EscapeString(e.ID),
			html.EscapeString(e.ID),
			html.EscapeString(e.Kind),
			html.EscapeString(e.Target),
			html.EscapeString(e.Model),
			verdictClass,
			html.EscapeString(string(e.Verdict)),
			e.TotalSeeds,
			e.Operations,
			html.EscapeString(deltaStr),
			html.EscapeString(shortFP(e.Fingerprint)),
			attStr,
			html.EscapeString(e.Timestamp),
		)
	}

	b.WriteString(`
    </tbody>
  </table>
  <pre class="cli"># Ingest a single-seed report or multi-seed campaign manifest into the registry:
curl -sS -X POST http://`)
	b.WriteString(html.EscapeString(s.cfg.ListenAddr))
	b.WriteString(`/api/v1/ingest --data-binary @strife-out/report.json
curl -sS -X POST http://`)
	b.WriteString(html.EscapeString(s.cfg.ListenAddr))
	b.WriteString(`/api/v1/ingest --data-binary @strife-campaign/campaign.json</pre>
</main>
</body>
</html>`)
	_, _ = w.Write([]byte(b.String()))
}
