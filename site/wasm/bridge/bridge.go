// Package bridge adapts Strife's engine to the website's WebAssembly build.
//
// It holds no testing logic of its own: simulations, checks and signature
// verification run through the same packages as the CLI, so a visitor's
// browser reports the same verdicts and hashes as `strife` on Linux. What it
// adds is the plumbing a browser needs (bytes instead of file paths, JSON
// results) and a few limits that keep a tab responsive.
package bridge

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/explore"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/probe"
	"github.com/kemalhafizh/strife/pkg/report"
)

// Limits for a simulation in the browser. The CLI has none of these; they
// only stop a visitor from freezing a tab for minutes.
const (
	MaxDuration = 30 * time.Second
	MaxNodes    = 9
	MaxClients  = 50
)

// System is a built-in simulated system, as `strife systems` lists it.
type System struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Expect      string `json:"expect"`
	Hint        string `json:"hint,omitempty"`
}

// Systems lists the simulated systems the playground can run.
func Systems() []System {
	var out []System
	for _, s := range explore.Systems() {
		out = append(out, System{Name: s.Name, Description: s.Description, Expect: string(s.Expect), Hint: s.Hint})
	}
	return out
}

// FaultKinds lists every fault kind the simulator injects.
func FaultKinds() []string {
	out := make([]string, len(nemesis.AllKinds))
	for i, k := range nemesis.AllKinds {
		out[i] = string(k)
	}
	return out
}

// Models lists the consistency models Check accepts.
func Models() []string { return model.Names() }

// SimRequest describes one simulation. Zero fields take the CLI's defaults;
// Seed is used as given.
type SimRequest struct {
	System   string `json:"system"`
	Faults   string `json:"faults"` // "all" (default), "none" or a list such as "kill,io-stall"
	Seed     uint64 `json:"seed"`
	Nodes    int    `json:"nodes,omitempty"`
	Clients  int    `json:"clients,omitempty"`
	Duration string `json:"duration,omitempty"` // simulated length, e.g. "10s"
	Report   bool   `json:"report,omitempty"`   // also render report.html
}

// Fault is one entry of a fault schedule.
type Fault struct {
	ID    int    `json:"id"`
	Kind  string `json:"kind"`
	Start string `json:"start"`
	End   string `json:"end"`
	Nodes []int  `json:"nodes"`
}

// KeyFailure explains why one key's operations have no valid order.
type KeyFailure struct {
	Key     string `json:"key"`
	Summary string `json:"summary"`
}

// SimResult is the outcome of Simulate.
type SimResult struct {
	System       string        `json:"system"`
	Seed         uint64        `json:"seed"`
	Verdict      string        `json:"verdict"`
	Events       uint64        `json:"events"`
	Simulated    string        `json:"simulated"`
	Ops          history.Stats `json:"ops"`
	Faults       []Fault       `json:"faults"`
	Failures     []KeyFailure  `json:"failures,omitempty"`
	TraceHash    string        `json:"trace_hash"`
	HistoryHash  string        `json:"history_hash"`
	ScheduleHash string        `json:"schedule_hash"`
	Command      string        `json:"command"`
	ElapsedMS    int64         `json:"elapsed_ms"`
	ReportHTML   string        `json:"report_html,omitempty"`
}

// Simulate runs one deterministic simulation and checks it, as `strife sim`
// does for a single seed.
func Simulate(req SimRequest) (*SimResult, error) {
	cfg, err := req.config()
	if err != nil {
		return nil, err
	}
	res, err := explore.Run(cfg)
	if err != nil {
		return nil, err
	}
	// The CLI adds witnesses before it writes a report; they turn the
	// checker's explanation into a diagnosis such as "stale read: …".
	_ = checker.AddWitnesses(model.KV{}, res.History, &res.Check, checker.WitnessOptions{})
	out := &SimResult{
		System:       cfg.System,
		Seed:         cfg.Seed,
		Verdict:      string(res.Check.Verdict),
		Events:       res.Events,
		Simulated:    cfg.Duration.String(),
		Ops:          res.Check.Stats,
		Faults:       faults(res.Schedule),
		Failures:     failures(res.Check),
		TraceHash:    res.TraceHash,
		HistoryHash:  res.HistoryHash,
		ScheduleHash: res.ScheduleHash,
		Command:      command(cfg),
		ElapsedMS:    res.Elapsed.Milliseconds(),
	}
	if req.Report {
		r, err := report.New(report.KindSimulation, cfg.System, cfg.Seed, cfg, res.Schedule, res.History, res.Check, res.TraceHash)
		if err != nil {
			return nil, err
		}
		r.Events = res.Events
		r.Reproduce = out.Command
		r.Fingerprint = report.Fingerprint(r)
		if out.ReportHTML, err = renderHTML(r, res.History); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (req SimRequest) config() (explore.Config, error) {
	cfg := explore.DefaultConfig()
	if req.System != "" {
		cfg.System = req.System
	}
	cfg.Seed = req.Seed
	spec := req.Faults
	if spec == "" {
		spec = "all"
	}
	kinds, err := nemesis.ParseKinds(spec)
	if err != nil {
		return cfg, err
	}
	cfg.Faults = kinds
	if req.Nodes != 0 {
		cfg.Nodes = req.Nodes
	}
	if req.Clients != 0 {
		cfg.Clients = req.Clients
	}
	if req.Duration != "" {
		if cfg.Duration, err = time.ParseDuration(req.Duration); err != nil {
			return cfg, fmt.Errorf("duration: %w", err)
		}
	}
	switch {
	case cfg.Duration > MaxDuration:
		return cfg, fmt.Errorf("the browser runs at most %v of simulated time; use the CLI for longer runs", MaxDuration)
	case cfg.Nodes > MaxNodes:
		return cfg, fmt.Errorf("the browser simulates at most %d nodes; use the CLI for larger clusters", MaxNodes)
	case cfg.Clients > MaxClients:
		return cfg, fmt.Errorf("the browser runs at most %d clients; use the CLI for more", MaxClients)
	}
	return cfg, cfg.Validate()
}

// command is the shortest `strife sim` line that replays cfg.
func command(cfg explore.Config) string {
	d := explore.DefaultConfig()
	var b strings.Builder
	fmt.Fprintf(&b, "strife sim --system %s", cfg.System)
	if spec := faultSpec(cfg.Faults); spec != "all" {
		fmt.Fprintf(&b, " --faults %s", spec)
	}
	fmt.Fprintf(&b, " --seed %d", cfg.Seed)
	if cfg.Nodes != d.Nodes {
		fmt.Fprintf(&b, " --nodes %d", cfg.Nodes)
	}
	if cfg.Clients != d.Clients {
		fmt.Fprintf(&b, " --clients %d", cfg.Clients)
	}
	if cfg.Duration != d.Duration {
		fmt.Fprintf(&b, " --duration %v", cfg.Duration)
	}
	return b.String()
}

func faultSpec(kinds []nemesis.Kind) string {
	if len(kinds) == 0 {
		return "none"
	}
	if len(kinds) == len(nemesis.AllKinds) {
		all := true
		for i, k := range kinds {
			all = all && k == nemesis.AllKinds[i]
		}
		if all {
			return "all"
		}
	}
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	return strings.Join(names, ",")
}

func faults(s nemesis.Schedule) []Fault {
	out := make([]Fault, 0, len(s.Faults))
	for _, f := range s.Faults {
		out = append(out, Fault{ID: f.ID, Kind: string(f.Kind), Start: seconds(f.Start), End: seconds(f.End), Nodes: f.Nodes})
	}
	return out
}

// seconds formats a schedule offset the way the CLI prints it, e.g. "1.056s".
func seconds(d time.Duration) string { return fmt.Sprintf("%.3fs", d.Seconds()) }

func failures(rep checker.Report) []KeyFailure {
	var out []KeyFailure
	for _, k := range rep.Failures() {
		f := KeyFailure{Key: k.Key}
		if k.Counterexample != nil {
			f.Summary = k.Counterexample.Summary()
		}
		out = append(out, f)
	}
	return out
}

// CheckRequest selects how Check reads a history.
type CheckRequest struct {
	Name      string `json:"name"`      // file name shown in the report
	Model     string `json:"model"`     // kv (default), set, queue, counter, idgen, txn, append
	Isolation string `json:"isolation"` // for txn and append; default serializable
	Report    bool   `json:"report,omitempty"`
}

// CheckResult is the outcome of Check.
type CheckResult struct {
	Model       string        `json:"model"`
	Verdict     string        `json:"verdict"`
	Operations  int           `json:"operations"`
	Checked     int           `json:"checked"`
	Keys        int           `json:"keys"`
	Ops         history.Stats `json:"ops"`
	Failures    []KeyFailure  `json:"failures,omitempty"`
	Findings    []string      `json:"findings,omitempty"`
	HistoryHash string        `json:"history_hash"`
	Fingerprint string        `json:"fingerprint"`
	ElapsedMS   int64         `json:"elapsed_ms"`
	ReportHTML  string        `json:"report_html,omitempty"`
}

// Check verifies a recorded history.jsonl against a consistency model, as
// `strife check` does.
func Check(jsonl []byte, req CheckRequest) (*CheckResult, error) {
	name := req.Model
	if name == "" {
		name = "kv"
	}
	m, err := model.ByName(name)
	if err != nil {
		return nil, err
	}
	level := req.Isolation
	if level == "" {
		level = "serializable"
	}
	m = withIsolation(m, level)
	ops, err := history.ReadJSONL(bytes.NewReader(jsonl))
	if err != nil {
		return nil, err
	}
	start := time.Now()
	rep, err := checker.Check(m, ops, checker.Options{})
	if err != nil {
		return nil, err
	}
	_ = checker.AddWitnesses(m, ops, &rep, checker.WitnessOptions{})
	found := probe.Correlate(ops, rep, nemesis.Schedule{}, nil)
	target := req.Name
	if target == "" {
		target = "history.jsonl"
	}
	r, err := report.New(report.KindCheck, target, 0, map[string]string{"model": m.Name()}, nemesis.Schedule{}, ops, rep, "")
	if err != nil {
		return nil, err
	}
	r.Findings = found
	out := &CheckResult{
		Model:       m.Name(),
		Verdict:     string(rep.Verdict),
		Operations:  rep.Operations,
		Checked:     rep.Checked,
		Keys:        len(rep.Keys),
		Ops:         rep.Stats,
		Failures:    failures(rep),
		HistoryHash: r.HistoryHash,
		Fingerprint: r.Fingerprint,
		ElapsedMS:   time.Since(start).Milliseconds(),
	}
	for _, f := range found {
		out.Findings = append(out.Findings, fmt.Sprintf("[%s] %s", f.Confidence, f.Summary))
	}
	if req.Report {
		if out.ReportHTML, err = renderHTML(r, ops); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// withIsolation mirrors cmd/strife: the transactional models take the
// isolation level to check against.
func withIsolation(m model.Model, level string) model.Model {
	switch t := m.(type) {
	case model.TxnModel:
		t.Level = level
		return t
	case model.Append:
		t.Level = level
		return t
	}
	return m
}

func renderHTML(r *report.Report, ops []history.Op) (string, error) {
	var buf bytes.Buffer
	if err := report.HTML(&buf, r, ops); err != nil {
		return "", err
	}
	return buf.String(), nil
}
