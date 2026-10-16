package report

import (
	"encoding/json"
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

type timelineOp struct {
	Index   int    `json:"i"`
	Process int    `json:"p"`
	Key     string `json:"k,omitempty"`
	Func    string `json:"f"`
	Value   any    `json:"v,omitempty"`
	Value2  any    `json:"v2,omitempty"`
	Call    int64  `json:"c"`
	Return  int64  `json:"r"`
	Status  string `json:"s"`
	Desc    string `json:"d"`
	Mark    string `json:"m,omitempty"` // "stuck", "witness", "write", "concurrent"
}

type timelineFault struct {
	ID    int    `json:"id"`
	Kind  string `json:"kind"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Desc  string `json:"desc"`
}

type timelineKeyInfo struct {
	Key     string `json:"key"`
	Ops     int    `json:"ops"`
	Verdict string `json:"verdict"`
}

type timelinePayload struct {
	Target     string            `json:"target"`
	Seed       uint64            `json:"seed"`
	Verdict    string            `json:"verdict"`
	TMin       int64             `json:"t_min"`
	TMax       int64             `json:"t_max"`
	StuckIndex int               `json:"stuck_index"`
	FocusStart int64             `json:"focus_start"`
	FocusEnd   int64             `json:"focus_end"`
	HasAnomaly bool              `json:"has_anomaly"`
	Keys       []timelineKeyInfo `json:"keys"`
	Processes  []int             `json:"processes"`
	Ops        []timelineOp      `json:"ops"`
	Faults     []timelineFault   `json:"faults"`
}

type view struct {
	R            *Report
	Badge        string
	Bad          bool
	Bars         []bar
	Bands        []band
	Ticks        []tick
	Width        float64
	Height       float64
	LaneTop      float64
	Shown        int
	Total        int
	Fails        []checker.KeyResult
	FaultRows    []faultRow
	TimelineJSON template.HTML
	HasAnomaly   bool
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

// HTML renders a self-contained report with an interactive timeline analyzer
// and static fallback.
func HTML(w io.Writer, r *Report, ops []history.Op) error {
	v := &view{
		R:     r,
		Badge: badge(r.Verdict),
		Bad:   r.Verdict != checker.Linearizable,
		Width: svgWidth,
		Fails: r.Check.Failures(),
	}
	for _, f := range r.Schedule.Faults {
		v.FaultRows = append(v.FaultRows, faultRow{
			ID:          f.ID,
			Start:       f.Start.Round(time.Millisecond).String(),
			End:         f.End.Round(time.Millisecond).String(),
			Description: f.Describe(r.Schedule.Nodes),
		})
	}

	paired, err := history.Pair(ops)
	if err == nil {
		layout(v, paired)
		payload := buildTimelinePayload(r, paired, v.Fails)
		v.HasAnomaly = payload.HasAnomaly
		if j, err := json.Marshal(payload); err == nil {
			v.TimelineJSON = template.HTML(j)
		}
	}

	return page.Execute(w, v)
}

func buildTimelinePayload(r *Report, ops []history.Operation, fails []checker.KeyResult) timelinePayload {
	payload := timelinePayload{
		Target:     r.Target,
		Seed:       r.Seed,
		Verdict:    string(r.Verdict),
		StuckIndex: -1,
	}

	marked := make(map[int]string)
	var stuckOp *history.Operation

	for _, k := range fails {
		if ce := k.Counterexample; ce != nil {
			payload.HasAnomaly = true
			if payload.StuckIndex < 0 {
				payload.StuckIndex = ce.Stuck.Index
			}
			marked[ce.Stuck.Index] = "stuck"
			for _, w := range ce.Witness {
				if _, ok := marked[w.Index]; !ok {
					marked[w.Index] = "witness"
				}
			}
			for _, c := range ce.Concurrent {
				if _, ok := marked[c.Index]; !ok {
					marked[c.Index] = "concurrent"
				}
			}
		}
	}

	for i := range ops {
		if ops[i].Input.Index == payload.StuckIndex {
			stuckOp = &ops[i]
			break
		}
	}

	if stuckOp != nil && stuckOp.Input.F == history.FRead && stuckOp.Output.Value != nil {
		var latestWrite *history.Operation
		for i := range ops {
			o := &ops[i]
			if o.Status() == history.OK && o.Return < stuckOp.Call && (o.Input.F == history.FWrite || o.Input.F == history.FCas || o.Input.F == history.FAdd) {
				if latestWrite == nil || o.Return > latestWrite.Return {
					latestWrite = o
				}
			}
		}
		var writtenVal *int64
		if latestWrite != nil {
			writtenVal = latestWrite.Input.Value
			if writtenVal == nil {
				writtenVal = latestWrite.Output.Value
			}
		}
		if latestWrite != nil && writtenVal != nil && *writtenVal != *stuckOp.Output.Value {
			if _, ok := marked[latestWrite.Input.Index]; !ok {
				marked[latestWrite.Input.Index] = "write"
			}
		}
	}

	keySet := make(map[string]int)
	procSet := make(map[int]bool)

	var tMin, tMax int64
	if len(ops) > 0 {
		tMin = ops[0].Call
		tMax = ops[0].Call
	}

	var focusMin, focusMax int64
	firstMark := true

	for i := range ops {
		o := &ops[i]
		if o.Call < tMin {
			tMin = o.Call
		}
		end := o.Return
		if end == history.Infinity || end == 0 {
			end = o.Call
		}
		if end > tMax {
			tMax = end
		}

		if o.Input.Key != "" {
			keySet[o.Input.Key]++
		}
		procSet[o.Input.Process] = true

		if role, ok := marked[o.Input.Index]; ok && role != "" {
			if firstMark {
				focusMin = o.Call
				focusMax = end
				firstMark = false
			} else {
				if o.Call < focusMin {
					focusMin = o.Call
				}
				if end > focusMax {
					focusMax = end
				}
			}
		}
	}

	payload.TMin = tMin
	payload.TMax = tMax

	if payload.HasAnomaly && !firstMark {
		span := focusMax - focusMin
		if span <= 0 {
			span = int64(10 * time.Millisecond)
		}
		pad := span / 4
		if pad < int64(1*time.Millisecond) {
			pad = int64(1 * time.Millisecond)
		}
		payload.FocusStart = focusMin - pad
		if payload.FocusStart < tMin {
			payload.FocusStart = tMin
		}
		payload.FocusEnd = focusMax + pad
		if payload.FocusEnd > tMax {
			payload.FocusEnd = tMax
		}
	} else {
		payload.FocusStart = tMin
		payload.FocusEnd = tMax
	}

	keyVerdicts := make(map[string]string)
	for _, kr := range r.Check.Keys {
		keyVerdicts[kr.Key] = string(kr.Verdict)
	}

	for k, count := range keySet {
		vStatus := keyVerdicts[k]
		if vStatus == "" {
			vStatus = "ok"
		}
		payload.Keys = append(payload.Keys, timelineKeyInfo{
			Key:     k,
			Ops:     count,
			Verdict: vStatus,
		})
	}
	sort.Slice(payload.Keys, func(i, j int) bool {
		return payload.Keys[i].Key < payload.Keys[j].Key
	})

	for p := range procSet {
		payload.Processes = append(payload.Processes, p)
	}
	sort.Ints(payload.Processes)

	for _, f := range r.Schedule.Faults {
		payload.Faults = append(payload.Faults, timelineFault{
			ID:    f.ID,
			Kind:  string(f.Kind),
			Start: int64(f.Start),
			End:   int64(f.End),
			Desc:  f.Describe(r.Schedule.Nodes),
		})
	}

	payload.Ops = make([]timelineOp, 0, len(ops))
	for i := range ops {
		o := &ops[i]
		ret := o.Return
		if ret == history.Infinity {
			ret = -1
		}
		var val any
		if o.Output.Value != nil {
			val = *o.Output.Value
		} else if o.Input.Value != nil {
			val = *o.Input.Value
		}
		var val2 any
		if o.Input.Old != nil {
			val2 = *o.Input.Old
		}
		top := timelineOp{
			Index:   o.Input.Index,
			Process: o.Input.Process,
			Key:     o.Input.Key,
			Func:    string(o.Input.F),
			Value:   val,
			Value2:  val2,
			Call:    o.Call,
			Return:  ret,
			Status:  string(o.Status()),
			Desc:    o.Describe(),
			Mark:    marked[o.Input.Index],
		}
		payload.Ops = append(payload.Ops, top)
	}

	return payload
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
				if n := len(ce.Witness); n > 0 {
					stuck = ce.Witness[n-1].Index
				}
			}
			for _, w := range ce.Witness {
				marked[w.Index] = true
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
:root{
  --bg:#0e1015;--fg:#e4e6eb;--muted:#8b92a0;--card:#161920;--card-alt:#1c202a;--line:#262a36;
  --ok:#2ecc71;--fail:#7f8c8d;--info:#f39c12;--bad:#e74c3c;--band:#4a69bd;--accent:#3498db;
  --cyan:#00d2ff;--tooltip-bg:rgba(22,25,32,0.96);--tooltip-border:#3a4153;--shadow:0 8px 24px rgba(0,0,0,0.5);
}
@media (prefers-color-scheme:light){
  :root{
    --bg:#f8f9fa;--fg:#1a1d24;--muted:#5f6775;--card:#ffffff;--card-alt:#f1f3f7;--line:#e2e5eb;
    --ok:#27ae60;--fail:#95a5a6;--info:#d68910;--bad:#c0392b;--band:#3867d6;--accent:#2980b9;
    --cyan:#0984e3;--tooltip-bg:rgba(255,255,255,0.98);--tooltip-border:#cbd2de;--shadow:0 8px 24px rgba(0,0,0,0.12);
  }
}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.5 ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}
main{max-width:1320px;margin:0 auto;padding:24px 16px 80px}
h1{font-size:22px;margin:0 0 4px;font-weight:700;letter-spacing:-0.02em}
h2{font-size:16px;margin:28px 0 10px;font-weight:600;display:flex;align-items:center;gap:8px}
.card{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:18px;margin:12px 0;box-shadow:0 2px 8px rgba(0,0,0,0.04)}
.verdict{display:inline-block;font-weight:700;padding:5px 12px;border-radius:6px;background:var(--ok);color:#fff;letter-spacing:0.02em}
.verdict.bad{background:var(--bad)}
code,.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,monospace;font-size:12.5px;word-break:break-all}
table{border-collapse:collapse;width:100%}
td,th{text-align:left;padding:6px 10px;border-bottom:1px solid var(--line);vertical-align:top}
th{color:var(--muted);font-weight:600;font-size:12px;text-transform:uppercase;letter-spacing:0.04em}
.muted{color:var(--muted)}
ol li,ul li{margin:3px 0}

/* Toolbar & Controls */
.tl-toolbar{display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:12px;margin-bottom:12px;padding-bottom:12px;border-bottom:1px solid var(--line)}
.tl-group{display:flex;flex-wrap:wrap;align-items:center;gap:8px}
.tl-btn{background:var(--card-alt);color:var(--fg);border:1px solid var(--line);border-radius:6px;padding:5px 10px;font-size:12px;font-weight:5px;cursor:pointer;display:inline-flex;align-items:center;gap:4px;user-select:none;transition:all 0.15s ease}
.tl-btn:hover{background:var(--line);border-color:var(--muted)}
.tl-btn:active{transform:scale(0.98)}
.tl-btn.primary{background:var(--bad);color:#fff;border-color:var(--bad);font-weight:600}
.tl-btn.primary:hover{filter:brightness(1.1)}
.tl-btn.pulse{animation:tlPulse 2s infinite}
@keyframes tlPulse{0%{box-shadow:0 0 0 0 rgba(231,76,60,0.6)}70%{box-shadow:0 0 0 8px rgba(231,76,60,0)}100%{box-shadow:0 0 0 0 rgba(231,76,60,0)}}
.tl-select{background:var(--card-alt);color:var(--fg);border:1px solid var(--line);border-radius:6px;padding:4px 8px;font-size:12px;outline:none}
.tl-badge{font-size:11px;padding:2px 7px;border-radius:12px;font-weight:600;background:var(--card-alt);color:var(--muted);border:1px solid var(--line)}
.tl-checkbox-label{display:inline-flex;align-items:center;gap:5px;font-size:12px;color:var(--fg);cursor:pointer;user-select:none}
.tl-checkbox-label input{cursor:pointer;margin:0}

/* Canvas Viewport */
.tl-viewport-container{position:relative;background:var(--card-alt);border:1px solid var(--line);border-radius:8px;overflow:hidden}
#tl-main-canvas{display:block;cursor:crosshair;touch-action:none}
#tl-minimap-canvas{display:block;border-top:1px solid var(--line);background:rgba(0,0,0,0.15);cursor:pointer}

/* Floating Tooltip */
#tl-tooltip{position:fixed;display:none;z-index:9999;background:var(--tooltip-bg);color:var(--fg);border:1px solid var(--tooltip-border);border-radius:8px;padding:10px 14px;box-shadow:var(--shadow);pointer-events:none;font-size:12px;max-width:340px;backdrop-filter:blur(8px);-webkit-backdrop-filter:blur(8px);transition:opacity 0.1s ease}
.tt-title{font-weight:700;font-size:13px;margin-bottom:4px;display:flex;align-items:center;justify-content:space-between;gap:8px}
.tt-row{display:flex;justify-content:space-between;gap:12px;margin:2px 0;color:var(--muted)}
.tt-val{color:var(--fg);font-family:ui-monospace,SFMono-Regular,Menlo,monospace}
.badge-role{display:inline-block;padding:2px 6px;border-radius:4px;font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:0.04em}
.badge-stuck{background:var(--bad);color:#fff}
.badge-write{background:var(--cyan);color:#000}
.badge-witness{border:1px solid var(--bad);color:var(--bad)}
.badge-concurrent{border:1px dashed var(--info);color:var(--info)}

/* Operation Detail Modal */
dialog#tl-detail-dialog{border:1px solid var(--line);border-radius:10px;background:var(--card);color:var(--fg);padding:20px;max-width:540px;width:90%;box-shadow:var(--shadow)}
dialog#tl-detail-dialog::backdrop{background:rgba(0,0,0,0.65);backdrop-filter:blur(3px)}
.dialog-header{display:flex;justify-content:space-between;align-items:center;margin-bottom:14px}
.dialog-title{font-size:16px;font-weight:700;margin:0}
.dialog-close{background:none;border:none;color:var(--muted);font-size:20px;cursor:pointer;padding:0;line-height:1}
.dialog-close:hover{color:var(--fg)}
pre.dialog-code{background:var(--card-alt);border:1px solid var(--line);border-radius:6px;padding:10px;font-size:12px;overflow-x:auto;max-height:220px}

/* Static SVG Fallback */
svg text{fill:var(--muted);font:10px ui-monospace,monospace}
.ok{fill:var(--ok)}.fail{fill:var(--fail)}.info{fill:var(--info);opacity:.75}
.marked{stroke:var(--bad);stroke-width:2}
.band{fill:var(--band);opacity:.25}
.legend span{display:inline-block;width:10px;height:10px;border-radius:2px;margin:0 4px 0 12px;vertical-align:middle}
</style>
</head>
<body>
<main>
<h1>Strife report · {{.R.Target}}</h1>
<div class="muted">{{.R.Kind}} · seed {{.R.Seed}} · model {{.R.Check.Model}}{{if .R.GeneratedAt}} · {{.R.GeneratedAt}}{{end}}</div>

<div class="card">
  <span class="verdict{{if .Bad}} bad{{end}}">{{.Badge}}</span>
  <p>{{.R.Check.Stats.Invocations}} operations — {{.R.Check.Stats.OK}} ok, {{.R.Check.Stats.Fail}} failed, {{.R.Check.Stats.Info}} indeterminate; {{len .R.Schedule.Faults}} faults injected.</p>
  {{if .R.Reproduce}}<p>Reproduce: <code>{{.R.Reproduce}}</code></p>{{end}}
  <table>
    <tr><th>Fingerprint</th><td class="mono">{{.R.Fingerprint}}</td></tr>
    {{if .R.ScheduleHash}}<tr><th>Schedule SHA-256</th><td class="mono">{{.R.ScheduleHash}}</td></tr>{{end}}
    <tr><th>History SHA-256</th><td class="mono">{{.R.HistoryHash}}</td></tr>
    {{if .R.TraceHash}}<tr><th>Trace SHA-256</th><td class="mono">{{.R.TraceHash}} ({{.R.Events}} events)</td></tr>{{end}}
  </table>
</div>

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
</div>
{{end}}

{{range .Fails}}{{$key := .Key}}{{with .Counterexample}}
<h2>Counterexample · key <code>{{$key}}</code></h2>
<div class="card">
  <p><b>{{.Summary}}</b></p>
  {{if .Witness}}<p class="muted">Minimal witness: these {{len .Witness}} operations alone prove the violation.</p>
  <table><tr><th>Operation</th><th>Invoked</th><th>Completed</th></tr>
  {{range .Witness}}<tr><td><code>{{.Description}}</code></td><td>{{t .Call}}</td><td>{{t .Return}}</td></tr>{{end}}
  </table>{{end}}
  {{if .Diagnosis}}<p class="muted">Search: {{.Explanation}}</p>{{end}}
  <p><b>Stuck:</b> <code>{{.Stuck.Description}}</code> [{{t .Stuck.Call}} → {{t .Stuck.Return}}]</p>
  <p class="muted">Linearized prefix ({{len .Linearized}} ops, last 10 shown), ending in state <code>{{.StateAfter}}</code>:</p>
  <ol>{{range last 10 .Linearized}}<li><code>{{.Description}}</code> <span class="muted">[{{t .Call}} → {{t .Return}}]</span></li>{{end}}</ol>
  {{if .Concurrent}}<p class="muted">Concurrent candidates ({{len .Concurrent}}):</p>
  <ul>{{range first 10 .Concurrent}}<li><code>{{.Description}}</code> <span class="muted">[{{t .Call}} → {{t .Return}}]</span></li>{{end}}</ul>{{end}}
</div>
{{end}}{{end}}

<h2>Interactive Timeline Studio</h2>
<div class="card" id="interactive-timeline-app">
  <div class="tl-toolbar">
    <div class="tl-group">
      <button type="button" class="tl-btn" id="tl-btn-zoom-in" title="Zoom in (Wheel up)">➕ Zoom In</button>
      <button type="button" class="tl-btn" id="tl-btn-zoom-out" title="Zoom out (Wheel down)">➖ Zoom Out</button>
      <button type="button" class="tl-btn" id="tl-btn-reset" title="Reset view to full duration">🔄 Fit All</button>
      {{if .HasAnomaly}}
      <button type="button" class="tl-btn primary pulse" id="tl-btn-focus-anomaly" title="Jump & zoom to counterexample window">🎯 Focus Anomaly</button>
      {{end}}
      <span class="tl-badge" id="tl-status-badge">Loading ops…</span>
    </div>
    <div class="tl-group">
      <label class="tl-checkbox-label">
        <select class="tl-select" id="tl-select-key" title="Filter by register/data key">
          <option value="">All Keys</option>
        </select>
      </label>
      <label class="tl-checkbox-label">
        <select class="tl-select" id="tl-select-proc" title="Filter by client process">
          <option value="-1">All Processes</option>
        </select>
      </label>
      <label class="tl-checkbox-label" title="Toggle fault overlay bands">
        <input type="checkbox" id="tl-check-faults" checked> Faults
      </label>
      <label class="tl-checkbox-label" title="Show only counterexample operations">
        <input type="checkbox" id="tl-check-witness"> Anomaly Only
      </label>
    </div>
  </div>

  <div class="tl-viewport-container">
    <canvas id="tl-main-canvas" height="360"></canvas>
    <canvas id="tl-minimap-canvas" height="38"></canvas>
  </div>
  <div class="legend muted" style="margin-top:10px">
    <span style="background:var(--ok)"></span>ok
    <span style="background:var(--fail)"></span>fail
    <span style="background:var(--info)"></span>indeterminate
    <span style="border:2px solid var(--bad)"></span>stuck / witness
    <span style="border:2px solid var(--cyan)"></span>contradicting write
    <span style="background:var(--band);opacity:.4"></span>fault
  </div>
</div>

<noscript>
<h2>Timeline (Static Fallback)</h2>
<div class="card">
  <div class="legend muted">Showing {{.Shown}} of {{.Total}} operations
  <span style="background:var(--ok)"></span>ok<span style="background:var(--fail)"></span>fail<span style="background:var(--info)"></span>indeterminate<span style="border:2px solid var(--bad)"></span>in counterexample<span style="background:var(--band);opacity:.4"></span>fault</div>
  <svg viewBox="0 0 {{.Width}} {{.Height}}" width="100%" role="img" aria-label="operation timeline">
  {{range .Ticks}}<text x="{{.X}}" y="10" text-anchor="{{.Anchor}}">{{.Label}}</text>{{end}}
  {{range .Bands}}<rect class="band" x="{{.X}}" y="{{.Y}}" width="{{.W}}" height="12"><title>{{.Label}}</title></rect><text x="{{.X}}" y="{{.Y}}" dy="10" dx="2">{{.Kind}}</text>{{end}}
  {{range .Bars}}<rect class="{{.Class}}{{if .Marked}} marked{{end}}" x="{{.X}}" y="{{.Y}}" width="{{.W}}" height="7" rx="1"><title>{{.Title}}</title></rect>{{end}}
  </svg>
</div>
</noscript>

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

<!-- Tooltip Container -->
<div id="tl-tooltip"></div>

<!-- Operation Detail Modal -->
<dialog id="tl-detail-dialog">
  <div class="dialog-header">
    <h3 class="dialog-title" id="tl-dialog-title">Operation Details</h3>
    <button type="button" class="dialog-close" id="tl-dialog-close" aria-label="Close dialog">&times;</button>
  </div>
  <div id="tl-dialog-body"></div>
  <div style="margin-top:14px;display:flex;justify-content:flex-end;gap:8px">
    <button type="button" class="tl-btn" id="tl-dialog-copy">Copy JSON</button>
    <button type="button" class="tl-btn" id="tl-dialog-close-btn">Close</button>
  </div>
</dialog>

<!-- Timeline Raw Data Payload -->
<script id="strife-timeline-data" type="application/json">{{.TimelineJSON}}</script>

<!-- Timeline Interactive Controller -->
<script>
(function() {
  const dataEl = document.getElementById("strife-timeline-data");
  if (!dataEl || !dataEl.textContent) return;
  let data = null;
  try {
    data = JSON.parse(dataEl.textContent);
  } catch(e) {
    console.error("Failed to parse timeline data:", e);
    return;
  }
  if (!data || !data.ops || data.ops.length === 0) {
    const b = document.getElementById("tl-status-badge");
    if (b) b.textContent = "0 operations";
    return;
  }

  const allOps = data.ops;
  const faults = data.faults || [];
  const tMin = data.t_min;
  const tMax = data.t_max > tMin ? data.t_max : tMin + 1e9;

  let viewStart = tMin;
  let viewEnd = tMax;
  let activeKey = "";
  let activeProc = -1;
  let showFaults = true;
  let witnessOnly = false;
  let hoveredOp = null;
  let hoveredFault = null;
  let selectedOp = null;

  const mainCanvas = document.getElementById("tl-main-canvas");
  const miniCanvas = document.getElementById("tl-minimap-canvas");
  const statusBadge = document.getElementById("tl-status-badge");
  const tooltip = document.getElementById("tl-tooltip");
  const keySelect = document.getElementById("tl-select-key");
  const procSelect = document.getElementById("tl-select-proc");
  const faultsCheck = document.getElementById("tl-check-faults");
  const witnessCheck = document.getElementById("tl-check-witness");
  const dialog = document.getElementById("tl-detail-dialog");

  // Populate Key Dropdown
  if (keySelect && data.keys) {
    data.keys.forEach(k => {
      const opt = document.createElement("option");
      opt.value = k.key;
      opt.textContent = k.key + " (" + k.ops + " ops" + (k.verdict === "violation" ? " ❌" : "") + ")";
      keySelect.appendChild(opt);
    });
  }

  // Populate Process Dropdown
  if (procSelect && data.processes) {
    data.processes.forEach(p => {
      const opt = document.createElement("option");
      opt.value = p;
      opt.textContent = "p" + p;
      procSelect.appendChild(opt);
    });
  }

  // Theme Colors
  const isDark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
  const colors = {
    bg: isDark ? "#161920" : "#ffffff",
    cardAlt: isDark ? "#1c202a" : "#f1f3f7",
    line: isDark ? "#262a36" : "#e2e5eb",
    fg: isDark ? "#e4e6eb" : "#1a1d24",
    muted: isDark ? "#8b92a0" : "#5f6775",
    ok: isDark ? "#2ecc71" : "#27ae60",
    fail: isDark ? "#7f8c8d" : "#95a5a6",
    info: isDark ? "#f39c12" : "#d68910",
    bad: isDark ? "#e74c3c" : "#c0392b",
    cyan: isDark ? "#00d2ff" : "#0984e3",
    hairline: isDark ? "rgba(255,255,255,0.4)" : "rgba(0,0,0,0.35)",
    faultBg: isDark ? "rgba(91,108,255,0.25)" : "rgba(56,103,214,0.18)",
    faultBorder: isDark ? "rgba(91,108,255,0.6)" : "rgba(56,103,214,0.5)"
  };

  const faultColors = {
    partition: isDark ? "rgba(155,89,182,0.3)" : "rgba(142,68,173,0.2)",
    kill: isDark ? "rgba(231,76,60,0.3)" : "rgba(192,57,43,0.2)",
    pause: isDark ? "rgba(127,140,141,0.3)" : "rgba(108,122,137,0.2)",
    freeze: isDark ? "rgba(52,152,219,0.3)" : "rgba(41,128,185,0.2)",
    "io-stall": isDark ? "rgba(243,156,18,0.3)" : "rgba(214,137,16,0.2)"
  };

  function fmtTime(ns, span) {
    if (ns === -1 || ns === Infinity) return "∞";
    const s = ns / 1e9;
    if (span !== undefined && span < 2e6) {
      return (ns / 1e3).toFixed(1) + "µs";
    } else if (span !== undefined && span < 2e9) {
      return (ns / 1e6).toFixed(2) + "ms";
    }
    return s.toFixed(3) + "s";
  }

  function fmtDuration(ns) {
    if (ns < 0) return "?";
    if (ns < 1e3) return ns + "ns";
    if (ns < 1e6) return (ns / 1e3).toFixed(1) + "µs";
    if (ns < 1e9) return (ns / 1e6).toFixed(2) + "ms";
    return (ns / 1e9).toFixed(3) + "s";
  }

  function roundRect(ctx, x, y, w, h, r) {
    if (w < 2 * r) r = w / 2;
    if (h < 2 * r) r = h / 2;
    ctx.beginPath();
    ctx.moveTo(x + r, y);
    ctx.arcTo(x + w, y, x + w, y + h, r);
    ctx.arcTo(x + w, y + h, x, y + h, r);
    ctx.arcTo(x, y + h, x, y, r);
    ctx.arcTo(x, y, x + w, y, r);
    ctx.closePath();
  }

  let visibleOps = [];
  let renderedBands = [];

  function filterOps() {
    const list = [];
    for (let i = 0; i < allOps.length; i++) {
      const o = allOps[i];
      if (activeKey && o.k !== activeKey) continue;
      if (activeProc !== -1 && o.p !== activeProc) continue;
      if (witnessOnly && !o.m) continue;
      list.push(o);
    }
    return list;
  }

  function render() {
    const dpr = window.devicePixelRatio || 1;
    const width = mainCanvas.parentElement.clientWidth;
    const height = 360;

    mainCanvas.width = Math.floor(width * dpr);
    mainCanvas.height = Math.floor(height * dpr);
    mainCanvas.style.width = width + "px";
    mainCanvas.style.height = height + "px";

    const ctx = mainCanvas.getContext("2d");
    ctx.resetTransform();
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, width, height);

    const span = viewEnd - viewStart;
    const xOf = t => ((t - viewStart) / span) * width;

    // Time Axis Ticks
    const numTicks = Math.max(5, Math.min(10, Math.floor(width / 130)));
    ctx.strokeStyle = colors.line;
    ctx.fillStyle = colors.muted;
    ctx.font = "10px ui-monospace, SFMono-Regular, Menlo, monospace";
    ctx.lineWidth = 1;

    for (let i = 0; i <= numTicks; i++) {
      const t = viewStart + (span * i) / numTicks;
      const x = Math.floor(xOf(t));
      ctx.beginPath();
      ctx.moveTo(x, 22);
      ctx.lineTo(x, height);
      ctx.stroke();

      const label = fmtTime(t, span);
      ctx.textAlign = i === 0 ? "left" : (i === numTicks ? "right" : "center");
      ctx.fillText(label, x + (i === 0 ? 4 : (i === numTicks ? -4 : 0)), 15);
    }

    // Fault Bands
    renderedBands = [];
    let laneTop = 26;
    if (showFaults && faults.length > 0) {
      const faultRows = {};
      let rowCount = 0;
      faults.forEach(f => {
        if (faultRows[f.kind] === undefined) {
          faultRows[f.kind] = rowCount++;
        }
      });
      const bandHeight = 16;
      laneTop = 26 + rowCount * (bandHeight + 3) + 6;

      faults.forEach(f => {
        if (f.end < viewStart || f.start > viewEnd) return;
        const x0 = Math.max(0, xOf(f.start));
        const x1 = Math.min(width, xOf(f.end));
        const w = Math.max(2, x1 - x0);
        const y = 26 + faultRows[f.kind] * (bandHeight + 3);

        renderedBands.push({ f, x: x0, y, w, h: bandHeight });

        ctx.fillStyle = faultColors[f.kind] || colors.faultBg;
        roundRect(ctx, x0, y, w, bandHeight, 3);
        ctx.fill();
        ctx.strokeStyle = colors.faultBorder;
        ctx.stroke();

        ctx.fillStyle = colors.fg;
        ctx.font = "10px ui-monospace, SFMono-Regular, Menlo, monospace";
        ctx.textAlign = "left";
        ctx.save();
        ctx.beginPath();
        ctx.rect(x0, y, w, bandHeight);
        ctx.clip();
        ctx.fillText(f.kind + " #" + f.id + ": " + f.desc, Math.max(x0 + 4, 4), y + 12);
        ctx.restore();
      });
    }

    // Filter & Layout Operations
    const filtered = filterOps();
    visibleOps = [];
    for (let i = 0; i < filtered.length; i++) {
      const o = filtered[i];
      const end = o.r === -1 ? o.c + 1e6 : o.r;
      if (end >= viewStart && o.c <= viewEnd) {
        visibleOps.push(o);
      }
    }

    const laneH = 12;
    const laneEnd = [];
    visibleOps.forEach(o => {
      const end = o.r === -1 ? o.c + 1e6 : o.r;
      let lane = -1;
      for (let l = 0; l < laneEnd.length; l++) {
        if (laneEnd[l] < o.c) {
          lane = l;
          break;
        }
      }
      if (lane < 0) {
        lane = laneEnd.length;
        laneEnd.push(end);
      } else {
        laneEnd[lane] = end;
      }
      o._lane = lane;
    });

    // Draw Operation Bars
    visibleOps.forEach(o => {
      const end = o.r === -1 ? o.c + 1e6 : o.r;
      const x0 = xOf(o.c);
      const x1 = xOf(end);
      const w = Math.max(2, x1 - x0);
      const y = laneTop + o._lane * (laneH + 2);
      const h = laneH;

      o._x = x0;
      o._y = y;
      o._w = w;
      o._h = h;

      if (y + h > height) return;

      // Base bar
      ctx.fillStyle = colors[o.s] || colors.ok;
      roundRect(ctx, x0, y, w, h, 2);
      ctx.fill();

      // Highlight counterexample marks
      if (o.m) {
        ctx.save();
        if (o.m === "stuck") {
          ctx.strokeStyle = colors.bad;
          ctx.lineWidth = 2.5;
          ctx.shadowColor = colors.bad;
          ctx.shadowBlur = 6;
          roundRect(ctx, x0 - 1, y - 1, w + 2, h + 2, 3);
          ctx.stroke();
        } else if (o.m === "write") {
          ctx.strokeStyle = colors.cyan;
          ctx.lineWidth = 2;
          roundRect(ctx, x0 - 0.5, y - 0.5, w + 1, h + 1, 2);
          ctx.stroke();
        } else if (o.m === "witness") {
          ctx.strokeStyle = colors.bad;
          ctx.lineWidth = 1.5;
          roundRect(ctx, x0, y, w, h, 2);
          ctx.stroke();
        } else if (o.m === "concurrent") {
          ctx.strokeStyle = colors.info;
          ctx.lineWidth = 1.5;
          ctx.setLineDash([2, 2]);
          roundRect(ctx, x0, y, w, h, 2);
          ctx.stroke();
        }
        ctx.restore();
      }

      // Bar text label if wide enough
      if (w > 32) {
        ctx.fillStyle = "#ffffff";
        ctx.font = "9px ui-monospace, SFMono-Regular, Menlo, monospace";
        ctx.textAlign = "left";
        ctx.save();
        ctx.beginPath();
        ctx.rect(x0 + 2, y, w - 4, h);
        ctx.clip();
        const lbl = "p" + o.p + " " + o.f + (o.v !== undefined ? " " + o.v : "");
        ctx.fillText(lbl, x0 + 3, y + 9);
        ctx.restore();
      }
    });

    renderMinimap(width);

    // Update Status Badge
    if (statusBadge) {
      const dur = fmtDuration(span);
      statusBadge.textContent = visibleOps.length + " ops visible · span: " + dur;
    }
  }

  function renderMinimap(width) {
    const dpr = window.devicePixelRatio || 1;
    const height = 38;
    miniCanvas.width = Math.floor(width * dpr);
    miniCanvas.height = Math.floor(height * dpr);
    miniCanvas.style.width = width + "px";
    miniCanvas.style.height = height + "px";

    const ctx = miniCanvas.getContext("2d");
    ctx.resetTransform();
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, width, height);

    const totalSpan = tMax - tMin;
    const mx = t => ((t - tMin) / totalSpan) * width;

    // Mini op dots
    ctx.fillStyle = colors.muted;
    for (let i = 0; i < allOps.length; i += 3) {
      const o = allOps[i];
      const x = mx(o.c);
      ctx.fillRect(x, 6, 1.5, 12);
    }

    // Mini marked ops
    allOps.forEach(o => {
      if (o.m) {
        ctx.fillStyle = o.m === "stuck" || o.m === "witness" ? colors.bad : colors.cyan;
        ctx.fillRect(mx(o.c) - 1, 3, 3, 18);
      }
    });

    // Viewport Handle
    const vx0 = Math.max(0, mx(viewStart));
    const vx1 = Math.min(width, mx(viewEnd));
    const vw = Math.max(6, vx1 - vx0);

    ctx.fillStyle = "rgba(52, 152, 219, 0.25)";
    ctx.fillRect(vx0, 0, vw, height);
    ctx.strokeStyle = colors.accent;
    ctx.lineWidth = 1.5;
    ctx.strokeRect(vx0, 0.5, vw, height - 1);
  }

  // Interactivity: Zoom & Pan
  let isDragging = false;
  let dragStartX = 0;
  let dragStartTime = 0;

  mainCanvas.addEventListener("mousedown", e => {
    if (e.button !== 0) return;
    isDragging = true;
    dragStartX = e.clientX;
    dragStartTime = viewStart;
    mainCanvas.style.cursor = "grabbing";
  });

  window.addEventListener("mousemove", e => {
    if (isDragging) {
      const rect = mainCanvas.getBoundingClientRect();
      const dx = e.clientX - dragStartX;
      const span = viewEnd - viewStart;
      const dTime = (dx / rect.width) * span;
      let ns = dragStartTime - dTime;
      let ne = ns + span;
      if (ns < tMin) {
        ns = tMin;
        ne = ns + span;
      }
      if (ne > tMax) {
        ne = tMax;
        ns = ne - span;
      }
      viewStart = ns;
      viewEnd = ne;
      render();
    } else {
      handleHover(e);
    }
  });

  window.addEventListener("mouseup", () => {
    if (isDragging) {
      isDragging = false;
      mainCanvas.style.cursor = "crosshair";
    }
  });

  mainCanvas.addEventListener("wheel", e => {
    e.preventDefault();
    const rect = mainCanvas.getBoundingClientRect();
    const cursorRatio = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
    const span = viewEnd - viewStart;
    const zoomFactor = e.deltaY < 0 ? 0.75 : 1.33;
    const newSpan = Math.max(1000, Math.min(tMax - tMin, span * zoomFactor));

    let ns = Math.max(tMin, (viewStart + cursorRatio * span) - cursorRatio * newSpan);
    let ne = Math.min(tMax, ns + newSpan);
    if (ne - ns < newSpan) ns = Math.max(tMin, ne - newSpan);

    viewStart = ns;
    viewEnd = ne;
    render();
  }, { passive: false });

  // Mini-map Click / Scrub
  let isMinimapScrubbing = false;
  function updateMinimapScrub(e) {
    const rect = miniCanvas.getBoundingClientRect();
    const clickRatio = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
    const span = viewEnd - viewStart;
    const totalSpan = tMax - tMin;
    const centerT = tMin + clickRatio * totalSpan;

    let ns = centerT - span / 2;
    let ne = ns + span;
    if (ns < tMin) { ns = tMin; ne = ns + span; }
    if (ne > tMax) { ne = tMax; ns = ne - span; }
    viewStart = ns;
    viewEnd = ne;
    render();
  }

  miniCanvas.addEventListener("mousedown", e => {
    isMinimapScrubbing = true;
    updateMinimapScrub(e);
  });
  window.addEventListener("mousemove", e => {
    if (isMinimapScrubbing) updateMinimapScrub(e);
  });
  window.addEventListener("mouseup", () => {
    isMinimapScrubbing = false;
  });

  // Tooltip & Hover Detection
  function handleHover(e) {
    const rect = mainCanvas.getBoundingClientRect();
    const mx = e.clientX - rect.left;
    const my = e.clientY - rect.top;

    if (mx < 0 || mx > rect.width || my < 0 || my > rect.height) {
      tooltip.style.display = "none";
      return;
    }

    // Check fault bands
    for (let i = 0; i < renderedBands.length; i++) {
      const b = renderedBands[i];
      if (mx >= b.x && mx <= b.x + b.w && my >= b.y && my <= b.y + b.h) {
        showFaultTooltip(b.f, e.clientX, e.clientY);
        return;
      }
    }

    // Check operation bars
    for (let i = visibleOps.length - 1; i >= 0; i--) {
      const o = visibleOps[i];
      if (o._x !== undefined && mx >= o._x && mx <= o._x + o._w && my >= o._y && my <= o._y + o._h) {
        showOpTooltip(o, e.clientX, e.clientY);
        return;
      }
    }

    tooltip.style.display = "none";
  }

  function showOpTooltip(o, clientX, clientY) {
    let roleBadge = "";
    if (o.m === "stuck") roleBadge = '<span class="badge-role badge-stuck">🚨 Stuck Op</span>';
    else if (o.m === "write") roleBadge = '<span class="badge-role badge-write">⚡ Latest Write</span>';
    else if (o.m === "witness") roleBadge = '<span class="badge-role badge-witness">🔍 Witness</span>';
    else if (o.m === "concurrent") roleBadge = '<span class="badge-role badge-concurrent">⏳ Concurrent</span>';

    const lat = o.r !== -1 ? fmtDuration(o.r - o.c) : "∞ (timeout)";
    tooltip.innerHTML =
      '<div class="tt-title"><span>' + o.d + '</span>' + roleBadge + '</div>' +
      '<div class="tt-row"><span>Status:</span><span class="tt-val" style="color:var(--' + o.s + ')">' + o.s.toUpperCase() + '</span></div>' +
      '<div class="tt-row"><span>Process:</span><span class="tt-val">p' + o.p + '</span></div>' +
      (o.k ? '<div class="tt-row"><span>Key:</span><span class="tt-val">' + o.k + '</span></div>' : '') +
      '<div class="tt-row"><span>Call:</span><span class="tt-val">' + fmtTime(o.c) + '</span></div>' +
      '<div class="tt-row"><span>Return:</span><span class="tt-val">' + fmtTime(o.r) + '</span></div>' +
      '<div class="tt-row"><span>Latency:</span><span class="tt-val">' + lat + '</span></div>' +
      '<div style="margin-top:6px;font-size:10px;color:var(--muted)">Click to inspect JSON / timeline stream</div>';

    positionTooltip(clientX, clientY);
  }

  function showFaultTooltip(f, clientX, clientY) {
    const dur = fmtDuration(f.end - f.start);
    tooltip.innerHTML =
      '<div class="tt-title"><span>Fault #' + f.id + ': ' + f.kind + '</span><span class="badge-role badge-witness">FAULT</span></div>' +
      '<div class="tt-row"><span>Target:</span><span class="tt-val">' + f.desc + '</span></div>' +
      '<div class="tt-row"><span>Start:</span><span class="tt-val">' + fmtTime(f.start) + '</span></div>' +
      '<div class="tt-row"><span>End:</span><span class="tt-val">' + fmtTime(f.end) + '</span></div>' +
      '<div class="tt-row"><span>Duration:</span><span class="tt-val">' + dur + '</span></div>';
    positionTooltip(clientX, clientY);
  }

  function positionTooltip(clientX, clientY) {
    tooltip.style.display = "block";
    const tw = tooltip.offsetWidth;
    const th = tooltip.offsetHeight;
    let x = clientX + 14;
    let y = clientY + 14;
    if (x + tw > window.innerWidth - 12) x = clientX - tw - 12;
    if (y + th > window.innerHeight - 12) y = clientY - th - 12;
    tooltip.style.left = Math.max(10, x) + "px";
    tooltip.style.top = Math.max(10, y) + "px";
  }

  // Operation Click: Detail Dialog
  mainCanvas.addEventListener("click", e => {
    const rect = mainCanvas.getBoundingClientRect();
    const mx = e.clientX - rect.left;
    const my = e.clientY - rect.top;

    for (let i = visibleOps.length - 1; i >= 0; i--) {
      const o = visibleOps[i];
      if (o._x !== undefined && mx >= o._x && mx <= o._x + o._w && my >= o._y && my <= o._y + o._h) {
        openDetailDialog(o);
        return;
      }
    }
  });

  function openDetailDialog(o) {
    selectedOp = o;
    const title = document.getElementById("tl-dialog-title");
    const body = document.getElementById("tl-dialog-body");
    if (!dialog || !body) return;

    if (title) title.textContent = "Operation #" + o.i + " · " + o.d;
    const lat = o.r !== -1 ? fmtDuration(o.r - o.c) : "∞ (indeterminate)";
    const raw = JSON.stringify(o, null, 2);

    body.innerHTML =
      '<table style="margin-bottom:12px">' +
      '<tr><th>Index</th><td class="mono">#' + o.i + '</td></tr>' +
      '<tr><th>Process</th><td class="mono">p' + o.p + '</td></tr>' +
      (o.k ? '<tr><th>Key</th><td class="mono">' + o.k + '</td></tr>' : '') +
      '<tr><th>Status</th><td class="mono" style="color:var(--' + o.s + ')">' + o.s.toUpperCase() + '</td></tr>' +
      '<tr><th>Call Time</th><td class="mono">' + fmtTime(o.c) + ' (' + o.c + ' ns)</td></tr>' +
      '<tr><th>Return Time</th><td class="mono">' + fmtTime(o.r) + ' (' + o.r + ' ns)</td></tr>' +
      '<tr><th>Latency</th><td class="mono">' + lat + '</td></tr>' +
      (o.m ? '<tr><th>Anomaly Role</th><td><b>' + o.m.toUpperCase() + '</b></td></tr>' : '') +
      '</table>' +
      '<div style="font-weight:600;font-size:12px;margin-bottom:4px">Raw JSON Record:</div>' +
      '<pre class="dialog-code"><code>' + raw + '</code></pre>';

    dialog.showModal();
  }

  // Dialog Controls
  const closeBtn = document.getElementById("tl-dialog-close");
  const closeBtn2 = document.getElementById("tl-dialog-close-btn");
  const copyBtn = document.getElementById("tl-dialog-copy");
  if (closeBtn) closeBtn.onclick = () => dialog.close();
  if (closeBtn2) closeBtn2.onclick = () => dialog.close();
  if (copyBtn) {
    copyBtn.onclick = () => {
      if (!selectedOp) return;
      navigator.clipboard.writeText(JSON.stringify(selectedOp, null, 2)).then(() => {
        copyBtn.textContent = "Copied! ✓";
        setTimeout(() => copyBtn.textContent = "Copy JSON", 2000);
      });
    };
  }

  // Toolbar Button Handlers
  const btnZoomIn = document.getElementById("tl-btn-zoom-in");
  const btnZoomOut = document.getElementById("tl-btn-zoom-out");
  const btnReset = document.getElementById("tl-btn-reset");
  const btnFocus = document.getElementById("tl-btn-focus-anomaly");

  if (btnZoomIn) {
    btnZoomIn.onclick = () => {
      const span = viewEnd - viewStart;
      const mid = viewStart + span / 2;
      const newSpan = Math.max(1000, span * 0.65);
      viewStart = Math.max(tMin, mid - newSpan / 2);
      viewEnd = Math.min(tMax, viewStart + newSpan);
      render();
    };
  }

  if (btnZoomOut) {
    btnZoomOut.onclick = () => {
      const span = viewEnd - viewStart;
      const mid = viewStart + span / 2;
      const newSpan = Math.min(tMax - tMin, span * 1.5);
      viewStart = Math.max(tMin, mid - newSpan / 2);
      viewEnd = Math.min(tMax, viewStart + newSpan);
      render();
    };
  }

  if (btnReset) {
    btnReset.onclick = () => {
      viewStart = tMin;
      viewEnd = tMax;
      render();
    };
  }

  if (btnFocus) {
    btnFocus.onclick = () => {
      if (data.focus_start !== undefined && data.focus_end !== undefined) {
        viewStart = data.focus_start;
        viewEnd = data.focus_end;
        render();
      }
    };
  }

  // Filter Event Listeners
  if (keySelect) {
    keySelect.onchange = () => {
      activeKey = keySelect.value;
      render();
    };
  }

  if (procSelect) {
    procSelect.onchange = () => {
      activeProc = parseInt(procSelect.value, 10);
      render();
    };
  }

  if (faultsCheck) {
    faultsCheck.onchange = () => {
      showFaults = faultsCheck.checked;
      render();
    };
  }

  if (witnessCheck) {
    witnessCheck.onchange = () => {
      witnessOnly = witnessCheck.checked;
      render();
    };
  }

  // Keyboard Shortcuts
  window.addEventListener("keydown", e => {
    if (e.target && (e.target.tagName === "INPUT" || e.target.tagName === "SELECT")) return;
    if (e.key === "+" || e.key === "=") {
      if (btnZoomIn) btnZoomIn.click();
    } else if (e.key === "-" || e.key === "_") {
      if (btnZoomOut) btnZoomOut.click();
    } else if (e.key === "0") {
      if (btnReset) btnReset.click();
    } else if (e.key === "f" || e.key === "F") {
      if (btnFocus) btnFocus.click();
    }
  });

  window.addEventListener("resize", render);
  render();
})();
</script>
</main></body></html>
`))
