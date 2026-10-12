package report

import (
	"fmt"
	"html/template"
	"io"
	"sort"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
)

const maxDrawn = 1500

type bar struct {
	X, Y, W float64
	Class   string
	Title   string
	Marked  bool
}

type band struct {
	X, Y, W float64
	Label   string
	Kind    string
}

type tick struct {
	X      float64
	Label  string
	Anchor string
}

type view struct {
	R         *Report
	Badge     string
	Bad       bool
	Bars      []bar
	Bands     []band
	Ticks     []tick
	Width     float64
	Height    float64
	LaneTop   float64
	Shown     int
	Total     int
	Fails     []checker.KeyResult
	FaultRows []faultRow
}

type faultRow struct {
	ID          int
	Start, End  string
	Description string
}

const (
	svgWidth = 1200.0
	laneH    = 9.0
	bandH    = 14.0
)

// HTML renders a self-contained report with an operation timeline.
func HTML(w io.Writer, r *Report, ops []history.Op) error {
	v := &view{R: r, Badge: badge(r.Verdict), Bad: r.Verdict != checker.Linearizable, Width: svgWidth, Fails: r.Check.Failures()}
	for _, f := range r.Schedule.Faults {
		v.FaultRows = append(v.FaultRows, faultRow{f.ID, f.Start.Round(time.Millisecond).String(), f.End.Round(time.Millisecond).String(), f.Describe(r.Schedule.Nodes)})
	}
	paired, err := history.Pair(ops)
	if err == nil {
		layout(v, paired)
	}
	return page.Execute(w, v)
}

func layout(v *view, ops []history.Operation) {
	v.Total = len(ops)
	if len(ops) == 0 {
		return
	}
	marked := map[int]bool{}
	stuck := -1
	for _, k := range v.Fails {
		if ce := k.Counterexample; ce != nil {
			marked[ce.Stuck.Index] = true
			if stuck < 0 {
				stuck = ce.Stuck.Index
			}
			for _, s := range ce.Concurrent {
				marked[s.Index] = true
			}
		}
	}
	sort.SliceStable(ops, func(i, j int) bool { return ops[i].Call < ops[j].Call })
	lo, hi := 0, len(ops)
	if len(ops) > maxDrawn {
		center := 0
		for i, o := range ops {
			if o.Input.Index == stuck {
				center = i
			}
		}
		lo = center - maxDrawn/2
		if lo < 0 {
			lo = 0
		}
		hi = lo + maxDrawn
		if hi > len(ops) {
			hi = len(ops)
			lo = hi - maxDrawn
		}
	}
	shown := ops[lo:hi]
	v.Shown = len(shown)

	t0 := shown[0].Call
	// Indeterminate operations never return; draw them up to the moment the
	// client gave up on them so they do not hog a lane forever.
	drawEnd := func(o history.Operation) int64 {
		if o.Return != history.Infinity {
			return o.Return
		}
		if o.Output.Time > o.Call && o.Output.Time != history.Infinity {
			return o.Output.Time
		}
		return o.Call
	}
	var t1 int64
	for _, o := range shown {
		if end := drawEnd(o); end > t1 {
			t1 = end
		}
	}
	if t1 <= t0 {
		t1 = t0 + 1
	}
	span := float64(t1 - t0)
	x := func(t int64) float64 { return (float64(t-t0) / span) * svgWidth }

	// Fault bands: one row per fault lane, within the drawn window.
	rows := map[string]int{}
	for _, f := range v.R.Schedule.Faults {
		s, e := int64(f.Start), int64(f.End)
		if e < t0 || s > t1 {
			continue
		}
		if s < t0 {
			s = t0
		}
		if e > t1 {
			e = t1
		}
		row, ok := rows[string(f.Kind)]
		if !ok {
			row = len(rows)
			rows[string(f.Kind)] = row
		}
		v.Bands = append(v.Bands, band{X: x(s), W: maxf(x(e)-x(s), 1), Y: 18 + float64(row)*bandH, Label: f.Describe(v.R.Schedule.Nodes), Kind: string(f.Kind)})
	}
	v.LaneTop = 22 + float64(len(rows))*bandH

	// Greedy lane assignment keeps the picture compact.
	var laneEnd []int64
	for _, o := range shown {
		end := drawEnd(o)
		lane := -1
		for i, le := range laneEnd {
			if le < o.Call {
				lane = i
				break
			}
		}
		if lane < 0 {
			lane = len(laneEnd)
			laneEnd = append(laneEnd, 0)
		}
		laneEnd[lane] = end
		cls := string(o.Status())
		if cls == "" {
			cls = "info"
		}
		v.Bars = append(v.Bars, bar{
			X: x(o.Call), W: maxf(x(end)-x(o.Call), 1.5), Y: v.LaneTop + float64(lane)*laneH,
			Class: cls, Marked: marked[o.Input.Index],
			Title: fmt.Sprintf("%s  [%s → %s]", o.Describe(), fmtTime(o.Call), fmtTime(o.Return)),
		})
	}
	v.Height = v.LaneTop + float64(len(laneEnd))*laneH + 8
	for i := 0; i <= 10; i++ {
		t := t0 + int64(span*float64(i)/10)
		anchor := "middle"
		switch i {
		case 0:
			anchor = "start"
		case 10:
			anchor = "end"
		}
		v.Ticks = append(v.Ticks, tick{X: x(t), Label: fmtTime(t), Anchor: anchor})
	}
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

var page = template.Must(template.New("report").Funcs(template.FuncMap{
	"t": fmtTime,
	"last": func(n int, s []checker.Step) []checker.Step {
		if len(s) > n {
			return s[len(s)-n:]
		}
		return s
	},
	"first": func(n int, s []checker.Step) []checker.Step {
		if len(s) > n {
			return s[:n]
		}
		return s
	},
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Strife report · {{.R.Target}}</title>
<style>
:root{--bg:#f7f7f5;--fg:#1b1b1f;--muted:#666;--card:#fff;--line:#ddd;--ok:#3a9d5d;--fail:#9aa0a6;--info:#e0a030;--bad:#d1343b;--band:#5b6cff}
@media (prefers-color-scheme:dark){:root{--bg:#121316;--fg:#e8e8ea;--muted:#9a9aa2;--card:#1b1c20;--line:#2c2d33;--ok:#4fbf77;--fail:#5f646b;--info:#e9b34a;--bad:#ff5c63;--band:#8090ff}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.5 ui-sans-serif,system-ui,-apple-system,Segoe UI,sans-serif}
main{max-width:1240px;margin:0 auto;padding:24px 16px 64px}
h1{font-size:22px;margin:0 0 4px}h2{font-size:16px;margin:28px 0 8px}
.card{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:16px;margin:12px 0;overflow-x:auto}
.verdict{display:inline-block;font-weight:700;padding:4px 10px;border-radius:6px;background:var(--ok);color:#fff}
.verdict.bad{background:var(--bad)}
code,.mono{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12.5px;word-break:break-all}
table{border-collapse:collapse;width:100%}td,th{text-align:left;padding:4px 8px;border-bottom:1px solid var(--line);vertical-align:top}
.muted{color:var(--muted)}
svg text{fill:var(--muted);font:10px ui-monospace,monospace}
.ok{fill:var(--ok)}.fail{fill:var(--fail)}.info{fill:var(--info);opacity:.75}
.marked{stroke:var(--bad);stroke-width:2}
.band{fill:var(--band);opacity:.25}
.legend span{display:inline-block;width:10px;height:10px;border-radius:2px;margin:0 4px 0 12px;vertical-align:middle}
ol li{margin:2px 0}
</style></head><body><main>
<h1>Strife report · {{.R.Target}}</h1>
<div class="muted">{{.R.Kind}} · seed {{.R.Seed}} · model {{.R.Check.Model}}{{if .R.GeneratedAt}} · {{.R.GeneratedAt}}{{end}}</div>
<div class="card"><span class="verdict{{if .Bad}} bad{{end}}">{{.Badge}}</span>
<p>{{.R.Check.Stats.Invocations}} operations — {{.R.Check.Stats.OK}} ok, {{.R.Check.Stats.Fail}} failed, {{.R.Check.Stats.Info}} indeterminate; {{len .R.Schedule.Faults}} faults injected.</p>
{{if .R.Reproduce}}<p>Reproduce: <code>{{.R.Reproduce}}</code></p>{{end}}
<table>
<tr><th>Fingerprint</th><td class="mono">{{.R.Fingerprint}}</td></tr>
{{if .R.ScheduleHash}}<tr><th>Schedule SHA-256</th><td class="mono">{{.R.ScheduleHash}}</td></tr>{{end}}
<tr><th>History SHA-256</th><td class="mono">{{.R.HistoryHash}}</td></tr>
{{if .R.TraceHash}}<tr><th>Trace SHA-256</th><td class="mono">{{.R.TraceHash}} ({{.R.Events}} events)</td></tr>{{end}}
</table></div>

{{with .R.Shrink}}
<h2>Shrinking</h2>
<div class="card">
<p>Minimised from a failing run in {{.Runs}} simulations.</p>
<table><tr><th></th><th>Original</th><th>Minimal</th></tr>
<tr><th>Faults</th><td>{{.OriginalFaults}}</td><td><b>{{.MinimalFaults}}</b></td></tr>
<tr><th>Duration</th><td>{{.OriginalDuration}}</td><td>{{.MinimalDuration}}</td></tr>
<tr><th>Clients</th><td>{{.OriginalClients}}</td><td>{{.MinimalClients}}</td></tr>
<tr><th>Operations</th><td>{{.OriginalOps}}</td><td>{{.MinimalOps}}</td></tr>
</table>
<ul>{{range .Steps}}<li>{{.}}</li>{{end}}</ul>
{{if .BudgetExhausted}}<p class="muted">The run budget was exhausted: the scenario is smaller but may not be 1-minimal.</p>{{end}}
<p class="muted">Removing faults changes everything downstream of them, so the minimal scenario reproduces a violation of the same system, not necessarily the identical anomaly.</p>
</div>{{end}}

{{range .Fails}}{{$key := .Key}}{{with .Counterexample}}
<h2>Counterexample · key <code>{{$key}}</code></h2>
<div class="card">
<p>{{.Explanation}}</p>
<p><b>Stuck:</b> <code>{{.Stuck.Description}}</code> [{{t .Stuck.Call}} → {{t .Stuck.Return}}]</p>
<p class="muted">Linearized prefix ({{len .Linearized}} ops, last 10 shown), ending in state <code>{{.StateAfter}}</code>:</p>
<ol>{{range last 10 .Linearized}}<li><code>{{.Description}}</code> <span class="muted">[{{t .Call}} → {{t .Return}}]</span></li>{{end}}</ol>
{{if .Concurrent}}<p class="muted">Concurrent candidates ({{len .Concurrent}}):</p>
<ul>{{range first 10 .Concurrent}}<li><code>{{.Description}}</code> <span class="muted">[{{t .Call}} → {{t .Return}}]</span></li>{{end}}</ul>{{end}}
</div>{{end}}{{end}}

<h2>Timeline</h2>
<div class="card">
<div class="legend muted">Showing {{.Shown}} of {{.Total}} operations
<span style="background:var(--ok)"></span>ok<span style="background:var(--fail)"></span>fail<span style="background:var(--info)"></span>indeterminate<span style="border:2px solid var(--bad)"></span>in counterexample<span style="background:var(--band);opacity:.4"></span>fault</div>
<svg viewBox="0 0 {{.Width}} {{.Height}}" width="100%" role="img" aria-label="operation timeline">
{{range .Ticks}}<text x="{{.X}}" y="10" text-anchor="{{.Anchor}}">{{.Label}}</text>{{end}}
{{range .Bands}}<rect class="band" x="{{.X}}" y="{{.Y}}" width="{{.W}}" height="12"><title>{{.Label}}</title></rect><text x="{{.X}}" y="{{.Y}}" dy="10" dx="2">{{.Kind}}</text>{{end}}
{{range .Bars}}<rect class="{{.Class}}{{if .Marked}} marked{{end}}" x="{{.X}}" y="{{.Y}}" width="{{.W}}" height="7" rx="1"><title>{{.Title}}</title></rect>{{end}}
</svg></div>

<h2>Keys</h2>
<div class="card"><table><tr><th>Key</th><th>Operations</th><th>Verdict</th><th>Search steps</th></tr>
{{range .R.Check.Keys}}<tr><td><code>{{.Key}}</code></td><td>{{.Operations}}</td><td>{{.Verdict}}</td><td>{{.Steps}}</td></tr>{{end}}
</table></div>

{{if .FaultRows}}<h2>Fault schedule</h2>
<div class="card"><table><tr><th>#</th><th>Start</th><th>End</th><th>Fault</th></tr>
{{range .FaultRows}}<tr><td>{{.ID}}</td><td>{{.Start}}</td><td>{{.End}}</td><td>{{.Description}}</td></tr>{{end}}
</table></div>{{end}}
{{range .R.Notes}}<p class="muted">{{.}}</p>{{end}}
<p class="muted">Generated by Strife {{.R.Version}}</p>
</main></body></html>
`))
