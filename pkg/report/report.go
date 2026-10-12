// Package report produces the audit artifacts of a Strife run: a machine
// readable report.json, the raw history.jsonl and schedule.json needed to
// re-check or replay it, and human readable report.md / report.html.
//
// Every report carries a fingerprint: a SHA-256 over the configuration, the
// fault schedule, the history and (for simulations) the execution trace.
// Anyone holding the artifacts can recompute it and re-run the checker to
// verify the verdict independently.
package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// Version is the Strife version stamped into reports.
var Version = "0.1.0"

// Kinds of runs.
const (
	KindSimulation = "simulation"
	KindLive       = "live"
	KindCheck      = "check"
)

// Applied records what actually happened when a fault edge was executed on
// real infrastructure.
type Applied struct {
	FaultID int           `json:"fault_id"`
	Kind    nemesis.Kind  `json:"kind"`
	Start   bool          `json:"start"`
	Planned time.Duration `json:"planned"`
	Actual  time.Duration `json:"actual"`
	Errors  []string      `json:"errors,omitempty"`
}

// Report is the complete outcome of a run.
type Report struct {
	Tool         string           `json:"tool"`
	Version      string           `json:"version"`
	Kind         string           `json:"kind"`
	Target       string           `json:"target"`
	Seed         uint64           `json:"seed"`
	Verdict      checker.Verdict  `json:"verdict"`
	Fingerprint  string           `json:"fingerprint"`
	ScheduleHash string           `json:"schedule_hash,omitempty"`
	HistoryHash  string           `json:"history_hash"`
	TraceHash    string           `json:"trace_hash,omitempty"`
	Events       uint64           `json:"events,omitempty"`
	Nodes        int              `json:"nodes,omitempty"`
	Duration     time.Duration    `json:"duration,omitempty"`
	Config       json.RawMessage  `json:"config,omitempty"`
	Schedule     nemesis.Schedule `json:"schedule"`
	Applied      []Applied        `json:"applied,omitempty"`
	Check        checker.Report   `json:"check"`
	Reproduce    string           `json:"reproduce,omitempty"`
	GeneratedAt  string           `json:"generated_at,omitempty"`
	Notes        []string         `json:"notes,omitempty"`
}

// New assembles a report and computes its fingerprint.
func New(kind, target string, seed uint64, config any, sched nemesis.Schedule, ops []history.Op, check checker.Report, traceHash string) (*Report, error) {
	cfg, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	r := &Report{
		Tool:        "strife",
		Version:     Version,
		Kind:        kind,
		Target:      target,
		Seed:        seed,
		Verdict:     check.Verdict,
		HistoryHash: history.Hash(ops),
		TraceHash:   traceHash,
		Nodes:       sched.Nodes,
		Duration:    sched.Duration,
		Config:      cfg,
		Schedule:    sched,
		Check:       check,
	}
	if len(sched.Faults) > 0 || sched.Nodes > 0 {
		r.ScheduleHash = sched.Hash()
	}
	r.Fingerprint = Fingerprint(r)
	return r, nil
}

// Fingerprint hashes everything that determines a verdict.
func Fingerprint(r *Report) string {
	h := sha256.New()
	fmt.Fprintf(h, "strife-fingerprint-v1\n%s\n%s\n%d\n", r.Kind, r.Target, r.Seed)
	// Hash the compact form so re-indenting report.json keeps it verifiable.
	var cfg bytes.Buffer
	if err := json.Compact(&cfg, r.Config); err != nil {
		cfg.Write(r.Config)
	}
	h.Write(cfg.Bytes())
	fmt.Fprintf(h, "\n%s\n%s\n%s\n%s\n", r.ScheduleHash, r.HistoryHash, r.TraceHash, r.Check.Verdict)
	return hex.EncodeToString(h.Sum(nil))
}

// WriteDir writes every artifact into dir (created if needed) and returns the
// paths written.
func WriteDir(dir string, r *Report, ops []history.Op) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	write := func(name string, fn func(f *os.File) error) error {
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			return err
		}
		if err := fn(f); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		written = append(written, p)
		return nil
	}
	steps := []struct {
		name string
		fn   func(f *os.File) error
	}{
		{"report.json", func(f *os.File) error {
			enc := json.NewEncoder(f)
			enc.SetIndent("", "  ")
			return enc.Encode(r)
		}},
		{"history.jsonl", func(f *os.File) error { return history.WriteJSONL(f, ops) }},
		{"schedule.json", func(f *os.File) error {
			enc := json.NewEncoder(f)
			enc.SetIndent("", "  ")
			return enc.Encode(r.Schedule)
		}},
		{"report.md", func(f *os.File) error { return Markdown(f, r) }},
		{"report.html", func(f *os.File) error { return HTML(f, r, ops) }},
	}
	for _, s := range steps {
		if err := write(s.name, s.fn); err != nil {
			return written, err
		}
	}
	return written, nil
}
