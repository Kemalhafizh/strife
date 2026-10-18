package server

import (
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
)

// The dashboard is server-rendered HTML with no script: it works behind the
// strictest proxies, and html/template escapes everything a pushed report
// could inject.

const pageCSS = `
:root{--bg:#f7f8fa;--fg:#17202b;--muted:#5d6b7a;--card:#fff;--line:#dfe3e8;--ok:#127a4b;--bad:#b42318;--warn:#a15c07;--accent:#2457c5;--mono:ui-monospace,SFMono-Regular,Menlo,monospace;--st-good:#0ca30c;--st-bad:#d03b3b;--st-warn:#fab219}
@media (prefers-color-scheme:dark){:root{--bg:#0f141a;--fg:#e6ebf0;--muted:#94a3b3;--card:#161d25;--line:#26303b;--ok:#3ccf8e;--bad:#ff6b5e;--warn:#f0a63a;--accent:#7aa2ff}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,-apple-system,Segoe UI,sans-serif}
main{max-width:1100px;margin:0 auto;padding:24px 16px 64px}a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}
h1{font-size:22px;margin:0 0 4px}h2{font-size:16px;margin:28px 0 10px}.sub{color:var(--muted);font-size:13px}
.card{background:var(--card);border:1px solid var(--line);border-radius:10px;overflow-x:auto}
table{width:100%;border-collapse:collapse}th,td{padding:9px 12px;text-align:left;border-bottom:1px solid var(--line);white-space:nowrap}th{font-size:12px;color:var(--muted);font-weight:600;text-transform:uppercase;letter-spacing:.03em}tr:last-child td{border-bottom:0}
.mono{font-family:var(--mono);font-size:13px}.badge{display:inline-block;padding:1px 8px;border-radius:99px;font-size:12px;font-weight:600;border:1px solid currentColor}
.linearizable{color:var(--ok)}.violation{color:var(--bad)}.unknown{color:var(--warn)}.regression{color:var(--bad)}.fixed{color:var(--ok)}.new{color:var(--muted)}
.chartbox{padding:12px 12px 4px;direction:rtl}.chartbox>svg{direction:ltr;margin:0 auto 0 0}.chart,.strip{display:block}.legend{display:flex;flex-wrap:wrap;gap:6px 18px;margin:0 0 8px;font-size:13px;color:var(--muted)}.legend span{display:inline-flex;align-items:center;gap:6px}
.m-linearizable{fill:var(--st-good)}.m-violation{fill:var(--st-bad)}.m-unknown{fill:var(--st-warn)}.acc{fill:var(--accent)}.wash{fill:var(--accent);fill-opacity:.1}
.accl{fill:none;stroke:var(--accent);stroke-width:2;stroke-linejoin:round;stroke-linecap:round}.ring{stroke:var(--card);stroke-width:2}.hit{fill:transparent}
.ax,.grid{stroke:var(--line);stroke-width:1}.tk{fill:var(--muted);font-size:11px;font-variant-numeric:tabular-nums}.lbl{fill:var(--fg);font-size:12px;font-weight:600}
g:hover path{opacity:.75}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(170px,1fr));gap:12px}.stat{padding:14px}.stat b{display:block;font-size:22px}
dl{display:grid;grid-template-columns:max-content 1fr;gap:6px 16px;margin:0;padding:16px}dt{color:var(--muted)}dd{margin:0;overflow-wrap:anywhere}
.links a{margin-right:14px}td.wrap{white-space:normal;overflow-wrap:anywhere;min-width:220px}`

var funcs = template.FuncMap{
	"ago": func(t time.Time) string {
		d := time.Since(t)
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return strings.TrimSuffix(d.Truncate(time.Minute).String(), "0s") + " ago"
		case d < 48*time.Hour:
			return strings.TrimSuffix(d.Truncate(time.Hour).String(), "0m0s") + " ago"
		}
		return t.Format("2006-01-02")
	},
	"short": func(s string) string {
		if len(s) > 12 {
			return s[:12]
		}
		return s
	},
	"verdict": func(v checker.Verdict) string { return string(v) },
	"strip":   verdictStrip,
	"swatch":  func(v string) template.HTML { return legendSwatch(checker.Verdict(v)) },
}

var pages = template.Must(template.New("").Funcs(funcs).Parse(`
{{define "head"}}<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.}} · Strife</title><style>` + pageCSS + `</style></head><body><main>{{end}}
{{define "foot"}}</main></body></html>{{end}}

{{define "index"}}{{template "head" "Projects"}}
<h1>Strife run registry</h1><p class="sub">Projects you can read with the token you sent.</p>
<div class="card"><table><tr><th>Project</th><th>Runs</th><th>Series</th><th>Failing</th><th>Regressions</th><th>Last run</th></tr>
{{range .}}<tr><td><a href="/ui/{{.Name}}">{{.Name}}</a></td><td>{{.Runs}}</td><td>{{.Series}}</td><td>{{if .Failing}}<span class="violation">{{.Failing}}</span>{{else}}0{{end}}</td><td>{{.Regressions}}</td><td>{{if .Runs}}{{ago .LastRun}}{{else}}–{{end}}</td></tr>
{{else}}<tr><td colspan="6" class="sub">No project is readable without a token. Open /ui/&lt;project&gt; and sign in with its token as the password.</td></tr>{{end}}
</table></div>{{template "foot"}}{{end}}

{{define "project"}}{{template "head" .Name}}
<p class="sub"><a href="/">Projects</a></p><h1>{{.Name}}</h1>
<div class="grid"><div class="card stat"><b>{{.Summary.Runs}}</b><span class="sub">runs</span></div><div class="card stat"><b>{{.Summary.Series}}</b><span class="sub">series</span></div><div class="card stat"><b class="{{if .Summary.Failing}}violation{{end}}">{{.Summary.Failing}}</b><span class="sub">series failing now</span></div><div class="card stat"><b>{{.Summary.Regressions}}</b><span class="sub">regressions recorded</span></div></div>
<h2>Series</h2><p class="legend"><span>{{swatch "violation"}} violation</span><span>{{swatch "unknown"}} inconclusive</span><span>{{swatch "linearizable"}} passed</span></p><div class="card"><table><tr><th>Kind</th><th>Target</th><th>Latest</th><th>Recent (newest left)</th><th>Runs</th><th>Last run</th></tr>
{{range .Series}}<tr><td>{{.Kind}}</td><td class="wrap"><a href="/ui/{{$.Name}}/runs/{{.Latest.ID}}">{{.Target}}</a><div class="sub mono">{{.Name}}</div></td><td><span class="badge {{verdict .Latest.Verdict}}">{{.Latest.Verdict}}</span>{{if .Failing}} <span class="sub">×{{.Failing}}</span>{{end}}</td><td><a href="/ui/{{$.Name}}/series?name={{.Name}}" title="Trend of this series">{{strip .Recent}}</a></td><td>{{.Runs}}</td><td>{{ago .Latest.Received}}</td></tr>
{{else}}<tr><td colspan="6" class="sub">No runs yet. Push one with: strife push --server … --project {{$.Name}} strife-out/&lt;run&gt;</td></tr>{{end}}</table></div>
<h2>Recent runs</h2><div class="card"><table><tr><th>Run</th><th>Target</th><th>Verdict</th><th>Change</th><th>Ops</th><th>Signed</th><th>Commit</th><th>Received</th></tr>
{{range .Runs}}<tr><td class="mono"><a href="/ui/{{$.Name}}/runs/{{.ID}}">{{short .ID}}</a></td><td>{{.Kind}} · {{.Target}}</td><td><span class="badge {{verdict .Verdict}}">{{.Verdict}}</span></td><td>{{if .Change}}<span class="{{.Change}}">{{.Change}}</span>{{end}}</td><td>{{.Ops}}</td><td>{{if .Attested}}✓ {{.Signer}}{{else}}<span class="sub">no</span>{{end}}</td><td class="mono">{{short .Commit}}</td><td>{{ago .Received}}</td></tr>{{end}}
</table></div>{{template "foot"}}{{end}}

{{define "series"}}{{template "head" .Target}}
<p class="sub"><a href="/">Projects</a> / <a href="/ui/{{.Project}}">{{.Project}}</a></p>
<h1>{{.Kind}} · {{.Target}} <span class="badge {{verdict .Latest.Verdict}}">{{.Latest.Verdict}}</span></h1>
<p class="sub mono">{{.Series}}</p>
<div class="grid"><div class="card stat"><b>{{.Runs}}</b><span class="sub">runs in this series</span></div><div class="card stat"><b>{{.Passed}} of {{.Shown}}</b><span class="sub">passed, latest {{.Shown}} runs</span></div><div class="card stat"><b class="{{if .Failing}}violation{{end}}">{{if .Failing}}{{.Failing}}{{else}}0{{end}}</b><span class="sub">{{if .Failing}}runs failing in a row{{else}}failing now{{end}}</span></div><div class="card stat"><b>{{.Signed}} of {{.Shown}}</b><span class="sub">signed by a trusted key</span></div></div>
<h2>Verdict of each run</h2><p class="legend"><span>{{swatch "violation"}} violation</span><span>{{swatch "unknown"}} inconclusive</span><span>{{swatch "linearizable"}} passed</span><span>oldest left; hover a column for its run</span></p>
<div class="card chartbox">{{.VerdictChart}}</div>
{{if .ViolatingChart}}<h2>Violating seeds per campaign</h2><div class="card chartbox">{{.ViolatingChart}}</div>{{end}}
<h2>Operations per run</h2><div class="card chartbox">{{.OpsChart}}</div>
{{if lt .Shown .Runs}}<p class="sub">The charts show the latest {{.Shown}} of {{.Runs}} runs.</p>{{end}}
<h2>Runs</h2><div class="card"><table><tr><th>Run</th><th>Verdict</th><th>Change</th>{{if .Campaign}}<th>Violating seeds</th>{{end}}<th>Ops</th><th>Signed</th><th>Commit</th><th>Received</th></tr>
{{range .Table}}<tr><td class="mono"><a href="/ui/{{$.Project}}/runs/{{.ID}}">{{short .ID}}</a></td><td><span class="badge {{verdict .Verdict}}">{{.Verdict}}</span></td><td>{{if .Change}}<span class="{{.Change}}">{{.Change}}</span>{{end}}</td>{{if $.Campaign}}<td>{{.Violations}} of {{.Seeds}}</td>{{end}}<td>{{.Ops}}</td><td>{{if .Attested}}✓ {{.Signer}}{{else}}<span class="sub">no</span>{{end}}</td><td class="mono">{{short .Commit}}</td><td>{{ago .Received}}</td></tr>{{end}}
</table></div>{{template "foot"}}{{end}}

{{define "run"}}{{template "head" .Run.Target}}
<p class="sub"><a href="/">Projects</a> / <a href="/ui/{{.Project}}">{{.Project}}</a></p>
<h1>{{.Run.Kind}} · {{.Run.Target}} <span class="badge {{verdict .Run.Verdict}}">{{.Run.Verdict}}</span></h1>
<p class="links">{{if ne .Run.Kind "campaign"}}<a href="{{.API}}/report.html">Report timeline</a><a href="{{.API}}/files/report.json">report.json</a>{{else}}<a href="{{.API}}/files/campaign.json">campaign.json</a>{{end}}{{if .Run.HasHistory}}<a href="{{.API}}/files/history.jsonl">history.jsonl</a>{{end}}{{if .Run.Attested}}<a href="{{.API}}/files/bundle.json">bundle.json</a>{{end}}<a href="{{.API}}/dossier?format=html">Compliance dossier</a></p>
<div class="card"><dl>
<dt>Run</dt><dd class="mono">{{.Run.ID}}</dd>
<dt>Series</dt><dd class="mono"><a href="/ui/{{.Project}}/series?name={{.Run.Series}}">{{.Run.Series}}</a></dd>
<dt>Change</dt><dd>{{if .Run.Change}}<span class="{{.Run.Change}}">{{.Run.Change}}</span>{{else}}same outcome as before{{end}}{{if .Run.Previous}} (previous: <a class="mono" href="/ui/{{.Project}}/runs/{{.Run.Previous}}">{{short .Run.Previous}}</a>){{end}}</dd>
{{if .Run.Seeds}}<dt>Seeds</dt><dd>{{.Run.Seeds}} ({{.Run.Violations}} violating)</dd>{{else}}<dt>Seed</dt><dd>{{.Run.Seed}}</dd>{{end}}
<dt>Operations</dt><dd>{{.Run.Ops}}</dd>
<dt>Faults</dt><dd>{{range $i, $f := .Run.Faults}}{{if $i}}, {{end}}{{$f}}{{else}}none{{end}}</dd>
<dt>History</dt><dd>{{if .Run.HistoryVerified}}stored, SHA-256 matches the report{{else if eq .Run.Kind "campaign"}}kept with each seed's report{{else}}not pushed{{end}}</dd>
<dt>Signature</dt><dd>{{if .Run.Attested}}valid, by {{.Run.Signer}} (key {{.Run.KeyID}}){{else}}unsigned{{end}}</dd>
{{if or .Run.Commit .Run.Branch}}<dt>Commit</dt><dd class="mono">{{.Run.Commit}}{{if .Run.Branch}} on {{.Run.Branch}}{{end}}</dd>{{end}}
{{if .Run.Pusher}}<dt>Pushed by</dt><dd class="mono">{{.Run.Pusher}}</dd>{{end}}
{{if .Run.Source}}<dt>Source</dt><dd>{{if .Source}}<a href="{{.Source}}" rel="noopener noreferrer">{{.Run.Source}}</a>{{else}}{{.Run.Source}}{{end}}</dd>{{end}}
<dt>Received</dt><dd>{{.Run.Received.Format "2006-01-02 15:04:05 MST"}}</dd>
</dl></div>
{{with .Run.Diff}}<h2>Compared with the previous campaign</h2><div class="card"><table><tr><th>Seed</th><th>Change</th><th>Before</th><th>Now</th><th>Explanation</th></tr>
{{range .Regressed}}<tr><td>{{.Seed}}</td><td class="regression">regressed</td><td>{{.PrevVerdict}}</td><td>{{.CurrVerdict}}</td><td>{{.Explanation}}</td></tr>{{end}}
{{range .NewFailures}}<tr><td>{{.Seed}}</td><td class="regression">new failure</td><td>–</td><td>{{.CurrVerdict}}</td><td>{{.Explanation}}</td></tr>{{end}}
{{range .Fixed}}<tr><td>{{.Seed}}</td><td class="fixed">fixed</td><td>{{.PrevVerdict}}</td><td>{{.CurrVerdict}}</td><td></td></tr>{{end}}
<tr><td colspan="5" class="sub">{{.UnchangedPassCount}} seeds unchanged and passing</td></tr></table></div>{{end}}
{{template "foot"}}{{end}}
`))

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		s.logf("render %s: %v", name, err)
	}
}

func (s *Server) uiIndex(w http.ResponseWriter, r *http.Request) {
	var sums []ProjectSummary
	for _, p := range s.visible(r) {
		sums = append(sums, s.summary(p))
	}
	s.render(w, "index", sums)
}

func (s *Server) uiProject(w http.ResponseWriter, r *http.Request, p *project) {
	s.render(w, "project", map[string]any{
		"Name":    p.name,
		"Summary": s.summary(p),
		"Series":  s.seriesOf(p),
		"Runs":    s.store.list(p.name, nil, 50),
	})
}

// uiSeries shows one series' runs over time: their verdicts, violating
// seeds for campaigns, and operations, with every run in a table. A chart
// wider than a phone scrolls within its card and opens on the newest runs
// (a right-to-left scroll container, without script).
func (s *Server) uiSeries(w http.ResponseWriter, r *http.Request, p *project) {
	name := r.URL.Query().Get("name")
	all := s.store.list(p.name, func(run *Run) bool { return run.Series == name }, 0) // newest first
	if name == "" || len(all) == 0 {
		http.NotFound(w, r)
		return
	}
	shown := all[:min(len(all), trendWindow)]
	chrono := make([]*Run, len(shown)) // oldest first, as charts read
	passed, signed, failing := 0, 0, 0
	for i, run := range shown {
		chrono[len(shown)-1-i] = run
		if run.Verdict == checker.Linearizable {
			passed++
		}
		if run.Attested {
			signed++
		}
	}
	for _, run := range all {
		if run.Verdict != checker.Violation {
			break
		}
		failing++
	}
	latest := all[0]
	data := map[string]any{
		"Project": p.name, "Series": name, "Kind": latest.Kind, "Target": latest.Target, "Latest": latest,
		"Runs": len(all), "Shown": len(shown), "Passed": passed, "Signed": signed, "Failing": failing,
		"Campaign":     latest.Kind == "campaign",
		"VerdictChart": verdictChart(chrono),
		"OpsChart":     lineChart(chrono, func(r *Run) float64 { return float64(r.Ops) }, "Operations per run, oldest first"),
		"Table":        shown,
	}
	if latest.Kind == "campaign" {
		data["ViolatingChart"] = barChart(chrono, func(r *Run) float64 { return float64(r.Violations) }, "Violating seeds per campaign, oldest first")
	}
	s.render(w, "series", data)
}

func (s *Server) uiRun(w http.ResponseWriter, r *http.Request, p *project) {
	run := s.store.get(p.name, r.PathValue("id"))
	if run == nil || !validID.MatchString(run.ID) {
		http.NotFound(w, r)
		return
	}
	// Only http(s) sources become links: a pushed "javascript:" URL must
	// stay text (html/template would neutralise it anyway).
	src := ""
	if strings.HasPrefix(run.Source, "https://") || strings.HasPrefix(run.Source, "http://") {
		src = run.Source
	}
	s.render(w, "run", map[string]any{
		"Project": p.name,
		"Run":     run,
		"API":     "/api/v1/projects/" + p.name + "/runs/" + run.ID,
		"Source":  src,
	})
}
