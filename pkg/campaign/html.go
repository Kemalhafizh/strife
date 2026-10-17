package campaign

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"strings"

	"github.com/kemalhafizh/strife/pkg/checker"
)

type seedView struct {
	Seed          uint64
	Verdict       string
	Delta         string
	DeltaLabel    string
	TileClass     string
	Ops           int
	Faults        int
	MinimalFaults int
	Events        uint64
	TraceShort    string
	HistoryShort  string
	Explanation   string
	Reproduce     string
	ReportPath    string
	ElapsedMs     int64
}

type htmlView struct {
	M            *Manifest
	VerdictLabel string
	VerdictClass string
	FaultsList   string
	HasBaseline  bool
	Regressed    int
	Fixed        int
	Persistent   int
	NewFailures  int
	TraceDrifts  int
	Seeds        []seedView
	CampaignJSON template.JS
}

// WriteHTML renders a self-contained HTML campaign and regression dashboard to w.
func WriteHTML(w io.Writer, m *Manifest) error {
	deltaMap := make(map[uint64]ChangeKind)
	if m.Comparison != nil {
		for _, d := range m.Comparison.Regressed {
			deltaMap[d.Seed] = ChangeRegressed
		}
		for _, d := range m.Comparison.Fixed {
			deltaMap[d.Seed] = ChangeFixed
		}
		for _, d := range m.Comparison.PersistentFailures {
			deltaMap[d.Seed] = ChangePersistentFailure
		}
		for _, d := range m.Comparison.NewFailures {
			deltaMap[d.Seed] = ChangeNewFailure
		}
		for _, d := range m.Comparison.TraceDrifts {
			deltaMap[d.Seed] = ChangeTraceDrift
		}
	}

	faults := make([]string, len(m.Faults))
	for i, f := range m.Faults {
		faults[i] = string(f)
	}
	faultStr := strings.Join(faults, ", ")
	if faultStr == "" {
		faultStr = "none"
	}

	verdictLabel := strings.ToUpper(string(m.Verdict()))
	verdictClass := "ok"
	if m.Comparison != nil && m.Comparison.HasRegressions() {
		verdictLabel = fmt.Sprintf("REGRESSION (%d)", len(m.Comparison.Regressed))
		verdictClass = "bad"
	} else if m.Verdict() == checker.Violation {
		verdictClass = "bad"
	} else if m.Verdict() != checker.Linearizable {
		verdictClass = "warn"
	}

	v := htmlView{
		M:            m,
		VerdictLabel: verdictLabel,
		VerdictClass: verdictClass,
		FaultsList:   faultStr,
		HasBaseline:  m.Comparison != nil,
	}
	if m.Comparison != nil {
		v.Regressed = len(m.Comparison.Regressed)
		v.Fixed = len(m.Comparison.Fixed)
		v.Persistent = len(m.Comparison.PersistentFailures)
		v.NewFailures = len(m.Comparison.NewFailures)
		v.TraceDrifts = len(m.Comparison.TraceDrifts)
	}

	for _, s := range m.Seeds {
		dk := deltaMap[s.Seed]
		dLabel := "unchanged"
		tileClass := "tile-pass"
		if s.Verdict == checker.Violation {
			tileClass = "tile-fail"
		} else if s.Verdict != checker.Linearizable || s.Error != "" {
			tileClass = "tile-warn"
		}
		switch dk {
		case ChangeRegressed:
			dLabel = "REGRESSED"
			tileClass = "tile-regressed"
		case ChangeFixed:
			dLabel = "FIXED"
			tileClass = "tile-fixed"
		case ChangePersistentFailure:
			dLabel = "PERSISTENT"
			tileClass = "tile-fail"
		case ChangeNewFailure:
			dLabel = "NEW FAILURE"
			tileClass = "tile-fail"
		case ChangeTraceDrift:
			dLabel = "TRACE DRIFT"
			tileClass = "tile-drift"
		default:
			if s.Verdict == checker.Violation {
				dLabel = "VIOLATION"
			} else {
				dLabel = "PASS"
			}
		}
		expl := s.Explanation
		if expl == "" && s.Error != "" {
			expl = s.Error
		}
		v.Seeds = append(v.Seeds, seedView{
			Seed:          s.Seed,
			Verdict:       string(s.Verdict),
			Delta:         string(dk),
			DeltaLabel:    dLabel,
			TileClass:     tileClass,
			Ops:           s.Ops,
			Faults:        s.Faults,
			MinimalFaults: s.MinimalFaults,
			Events:        s.Events,
			TraceShort:    shortHash(s.TraceHash),
			HistoryShort:  shortHash(s.HistoryHash),
			Explanation:   expl,
			Reproduce:     s.Reproduce,
			ReportPath:    s.ReportPath,
			ElapsedMs:     s.ElapsedMs,
		})
	}

	if raw, err := json.Marshal(m); err == nil {
		v.CampaignJSON = template.JS(raw)
	}

	return campaignTmpl.Execute(w, v)
}

var campaignTmpl = template.Must(template.New("campaign").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Strife Campaign — {{.M.System}} ({{.M.CampaignID}})</title>
<style>
  :root {
    --bg: #0b0e14;
    --panel: #131822;
    --panel-alt: #19202e;
    --fg: #e6edf3;
    --muted: #8b949e;
    --line: #263042;
    --ok: #2ea043;
    --bad: #f85149;
    --regressed: #ff7b72;
    --fixed: #39c5cf;
    --drift: #a371f7;
    --warn: #d29922;
    --accent: #58a6ff;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; padding: 28px 36px;
    background: var(--bg); color: var(--fg);
    font: 14px/1.55 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  }
  code, pre, .mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
  header {
    display: flex; align-items: center; justify-content: space-between;
    flex-wrap: wrap; gap: 16px;
    padding-bottom: 18px; border-bottom: 1px solid var(--line);
  }
  h1 { font-size: 20px; margin: 0; font-weight: 600; }
  h2 { font-size: 13px; text-transform: uppercase; letter-spacing: .08em; color: var(--muted); margin: 26px 0 10px; }
  .badge {
    display: inline-block; padding: 5px 13px; border-radius: 6px;
    font-weight: 700; font-size: 12px; letter-spacing: .06em;
  }
  .badge.ok { background: rgba(46,160,67,.18); color: #56d364; border: 1px solid var(--ok); }
  .badge.bad { background: rgba(248,81,73,.18); color: #ff7b72; border: 1px solid var(--bad); }
  .badge.warn { background: rgba(210,153,34,.18); color: #e3b341; border: 1px solid var(--warn); }
  .kpis {
    display: grid; grid-template-columns: repeat(auto-fit, minmax(155px, 1fr));
    gap: 12px; margin-top: 18px;
  }
  .kpi {
    background: var(--panel); border: 1px solid var(--line); border-radius: 8px;
    padding: 12px 14px;
  }
  .kpi .k { font-size: 11px; text-transform: uppercase; color: var(--muted); letter-spacing: .05em; }
  .kpi .v { font-size: 20px; font-weight: 700; margin-top: 4px; }
  .kpi .v.ok { color: #56d364; }
  .kpi .v.bad { color: #ff7b72; }
  .kpi .v.fixed { color: var(--fixed); }
  .heatmap {
    display: flex; flex-wrap: wrap; gap: 5px;
    background: var(--panel); border: 1px solid var(--line); border-radius: 8px;
    padding: 14px;
  }
  .seed-tile {
    min-width: 38px; height: 28px; padding: 0 6px;
    border-radius: 4px; display: inline-flex; align-items: center; justify-content: center;
    font-size: 11px; font-weight: 600; cursor: pointer; user-select: none;
    border: 1px solid transparent; transition: transform .1s, border-color .1s;
  }
  .seed-tile:hover { transform: translateY(-1px); border-color: #fff; }
  .tile-pass { background: rgba(46,160,67,.22); color: #7ee787; border-color: rgba(46,160,67,.45); }
  .tile-fail { background: rgba(248,81,73,.25); color: #ffa198; border-color: rgba(248,81,73,.55); }
  .tile-regressed { background: rgba(248,81,73,.42); color: #fff; border-color: #ff7b72; box-shadow: 0 0 8px rgba(248,81,73,.45); }
  .tile-fixed { background: rgba(57,197,207,.22); color: #56d4dd; border-color: rgba(57,197,207,.5); }
  .tile-drift { background: rgba(163,113,247,.22); color: #d2a8ff; border-color: rgba(163,113,247,.5); }
  .tile-warn { background: rgba(210,153,34,.22); color: #e3b341; border-color: rgba(210,153,34,.5); }
  .toolbar {
    display: flex; flex-wrap: wrap; gap: 8px; align-items: center; justify-content: space-between;
    margin-bottom: 10px;
  }
  .pills { display: flex; flex-wrap: wrap; gap: 6px; }
  .pill {
    background: var(--panel); color: var(--fg); border: 1px solid var(--line);
    padding: 5px 11px; border-radius: 6px; font-size: 12px; cursor: pointer;
  }
  .pill.active { background: var(--accent); color: #0b0e14; border-color: var(--accent); font-weight: 600; }
  .search-input {
    background: var(--panel); color: var(--fg); border: 1px solid var(--line);
    padding: 6px 12px; border-radius: 6px; font-size: 12px; min-width: 240px;
  }
  table {
    width: 100%; border-collapse: collapse; background: var(--panel);
    border: 1px solid var(--line); border-radius: 8px; overflow: hidden; font-size: 13px;
  }
  th, td { text-align: left; padding: 8px 12px; border-bottom: 1px solid var(--line); vertical-align: top; }
  th { color: var(--muted); font-weight: 600; font-size: 11px; text-transform: uppercase; background: var(--panel-alt); }
  tr:hover td { background: rgba(88,166,255,.04); }
  .tag {
    display: inline-block; padding: 2px 7px; border-radius: 4px; font-size: 10px; font-weight: 700;
  }
  .tag.PASS { background: rgba(46,160,67,.18); color: #7ee787; }
  .tag.VIOLATION, .tag.PERSISTENT, .tag.NEW { background: rgba(248,81,73,.2); color: #ffa198; }
  .tag.REGRESSED { background: #f85149; color: #fff; }
  .tag.FIXED { background: rgba(57,197,207,.2); color: #56d4dd; }
  .tag.TRACE { background: rgba(163,113,247,.2); color: #d2a8ff; }
  .repro-code {
    display: block; font-size: 11px; color: var(--muted); word-break: break-all; margin-top: 4px;
  }
  a { color: var(--accent); text-decoration: none; }
  a:hover { text-decoration: underline; }
</style>
</head>
<body>
<header>
  <div>
    <h1>Strife Campaign · <span class="mono">{{.M.System}}</span></h1>
    <div style="color:var(--muted); font-size:12px; margin-top:3px">
      Campaign <span class="mono">{{.M.CampaignID}}</span>
      {{if .M.Commit}} · Commit <span class="mono">{{.M.Commit}}</span>{{end}}
      {{if .M.Branch}} ({{.M.Branch}}){{end}}
      {{if .HasBaseline}}{{if .M.Comparison.BaselineCommit}} · vs Baseline <span class="mono">{{.M.Comparison.BaselineCommit}}</span>{{end}}{{end}}
      · Faults: <span class="mono">{{.FaultsList}}</span>
    </div>
  </div>
  <span class="badge {{.VerdictClass}}">{{.VerdictLabel}}</span>
</header>

<div class="kpis">
  <div class="kpi"><div class="k">Total Seeds</div><div class="v mono">{{.M.TotalSeeds}}</div></div>
  <div class="kpi"><div class="k">Linearizable</div><div class="v mono ok">{{.M.Passed}}</div></div>
  <div class="kpi"><div class="k">Violations</div><div class="v mono {{if gt .M.Violations 0}}bad{{end}}">{{.M.Violations}}</div></div>
  {{if .HasBaseline}}
  <div class="kpi"><div class="k">Regressed</div><div class="v mono {{if gt .Regressed 0}}bad{{end}}">{{.Regressed}}</div></div>
  <div class="kpi"><div class="k">Fixed</div><div class="v mono fixed">{{.Fixed}}</div></div>
  <div class="kpi"><div class="k">Trace Drifts</div><div class="v mono">{{.TraceDrifts}}</div></div>
  {{end}}
  <div class="kpi"><div class="k">Simulated Events</div><div class="v mono">{{.M.TotalEvents}}</div></div>
  <div class="kpi"><div class="k">Client Operations</div><div class="v mono">{{.M.TotalOps}}</div></div>
  <div class="kpi"><div class="k">Wall Duration</div><div class="v mono">{{.M.DurationMs}} ms</div></div>
</div>

<h2>Seed Heatmap</h2>
<div class="heatmap" id="seed-heatmap">
  {{range .Seeds}}
  <div class="seed-tile {{.TileClass}} mono" data-seed="{{.Seed}}" title="Seed {{.Seed}}: {{.DeltaLabel}} ({{.Ops}} ops, {{.Faults}} faults)">
    #{{.Seed}}
  </div>
  {{end}}
</div>

<h2>Seed Ledger &amp; Regression Analysis</h2>
<div class="toolbar">
  <div class="pills" id="filter-pills">
    <button class="pill active" data-filter="all">All ({{.M.TotalSeeds}})</button>
    <button class="pill" data-filter="violation">Violations ({{.M.Violations}})</button>
    {{if .HasBaseline}}
    <button class="pill" data-filter="regressed">Regressed ({{.Regressed}})</button>
    <button class="pill" data-filter="fixed">Fixed ({{.Fixed}})</button>
    <button class="pill" data-filter="trace_drift">Trace Drift ({{.TraceDrifts}})</button>
    {{end}}
    <button class="pill" data-filter="linearizable">Linearizable ({{.M.Passed}})</button>
  </div>
  <input type="search" id="seed-search" class="search-input" placeholder="Filter by seed, hash, or counterexample...">
</div>

<table id="seed-table">
  <thead>
    <tr>
      <th>Seed</th>
      <th>Verdict</th>
      <th>Status / Delta</th>
      <th>Ops / Faults</th>
      <th>Events</th>
      <th>Trace Hash</th>
      <th>Diagnosis &amp; Reproduction</th>
    </tr>
  </thead>
  <tbody>
    {{range .Seeds}}
    <tr class="seed-row" id="seed-row-{{.Seed}}" data-seed="{{.Seed}}" data-verdict="{{.Verdict}}" data-delta="{{.Delta}}">
      <td class="mono"><strong>#{{.Seed}}</strong></td>
      <td class="mono">{{.Verdict}}</td>
      <td><span class="tag {{.DeltaLabel}}">{{.DeltaLabel}}</span></td>
      <td class="mono">{{.Ops}} ops / {{.Faults}} faults{{if gt .MinimalFaults 0}} (min: {{.MinimalFaults}}){{end}}</td>
      <td class="mono">{{.Events}}</td>
      <td class="mono">{{.TraceShort}}</td>
      <td>
        {{if .Explanation}}<div><strong>{{.Explanation}}</strong></div>{{end}}
        {{if .ReportPath}}<div><a href="{{.ReportPath}}">Open Interactive Timeline Report →</a></div>{{end}}
        <code class="repro-code">{{.Reproduce}}</code>
      </td>
    </tr>
    {{end}}
  </tbody>
</table>

<script id="campaign-data" type="application/json">{{.CampaignJSON}}</script>
<script>
(function() {
  var pills = document.querySelectorAll('#filter-pills .pill');
  var search = document.getElementById('seed-search');
  var rows = document.querySelectorAll('#seed-table tbody .seed-row');
  var tiles = document.querySelectorAll('#seed-heatmap .seed-tile');
  var activeFilter = 'all';

  function applyFilter() {
    var q = (search ? search.value : '').toLowerCase().trim();
    for (var i = 0; i < rows.length; i++) {
      var r = rows[i];
      var verdict = r.getAttribute('data-verdict') || '';
      var delta = r.getAttribute('data-delta') || '';
      var text = r.textContent.toLowerCase();
      var matchFilter = (activeFilter === 'all') ||
        (activeFilter === verdict) ||
        (activeFilter === delta);
      var matchSearch = !q || text.indexOf(q) !== -1;
      r.style.display = (matchFilter && matchSearch) ? '' : 'none';
    }
  }

  for (var i = 0; i < pills.length; i++) {
    pills[i].addEventListener('click', function() {
      for (var j = 0; j < pills.length; j++) pills[j].classList.remove('active');
      this.classList.add('active');
      activeFilter = this.getAttribute('data-filter') || 'all';
      applyFilter();
    });
  }

  if (search) {
    search.addEventListener('input', applyFilter);
  }

  for (var k = 0; k < tiles.length; k++) {
    tiles[k].addEventListener('click', function() {
      var seed = this.getAttribute('data-seed');
      var target = document.getElementById('seed-row-' + seed);
      if (target) {
        activeFilter = 'all';
        for (var j = 0; j < pills.length; j++) {
          pills[j].classList.toggle('active', pills[j].getAttribute('data-filter') === 'all');
        }
        applyFilter();
        target.scrollIntoView({ behavior: 'smooth', block: 'center' });
        target.style.outline = '2px solid #58a6ff';
        setTimeout(function() { target.style.outline = ''; }, 1800);
      }
    });
  }
})();
</script>
</body>
</html>
`))
