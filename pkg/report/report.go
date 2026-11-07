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
	"github.com/kemalhafizh/strife/internal/stats"
	"github.com/kemalhafizh/strife/internal/version"
	"os"
	"path/filepath"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/probe"
)

// Version is the Strife version stamped into reports.
var Version = version.Version

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
	// Jitter is |Actual - Planned|: how late the scheduler woke up.
	Jitter time.Duration `json:"jitter,omitempty"`
	// Landed is when every node had confirmed the transition, and Latency
	// is Landed - Planned: how late the fault actually took effect.
	Landed  time.Duration `json:"landed,omitempty"`
	Latency time.Duration `json:"latency,omitempty"`
	Method  string        `json:"method,omitempty"`
	Errors  []string      `json:"errors,omitempty"`
}

// InjectionTiming summarises how late fault transitions took effect.
// Transitions that reported errors are excluded: they did not land.
func InjectionTiming(applied []Applied) stats.Summary {
	var d []time.Duration
	for _, a := range applied {
		if len(a.Errors) == 0 && a.Landed > 0 {
			d = append(d, a.Latency)
		}
	}
	return stats.Summarize(d)
}

// FaultRecovery records the measured recovery time after a single fault healed.
type FaultRecovery struct {
	FaultID     int           `json:"fault_id"`
	Kind        nemesis.Kind  `json:"kind"`
	HealedAt    time.Duration `json:"healed_at"`
	Recovered   time.Duration `json:"recovered_at,omitempty"`
	MTTR        time.Duration `json:"mttr"`
	RecoveredOK bool          `json:"recovered_ok"`
}

// ResilienceMetrics quantifies service availability and post-fault recovery
// latency (MTTR) from the recorded operation history and fault schedule.
type ResilienceMetrics struct {
	OverallAvailability       float64         `json:"overall_availability"`
	HealthyWindowAvailability float64         `json:"healthy_window_availability"`
	FaultWindowAvailability   float64         `json:"fault_window_availability"`
	MTTR                      stats.Summary   `json:"mttr"`
	Recoveries                []FaultRecovery `json:"recoveries,omitempty"`
}

// ComputeResilience calculates availability ratios (during vs outside fault
// windows) and post-heal recovery latency (MTTR) from the schedule, applied
// transitions, and recorded operation history.
func ComputeResilience(sched nemesis.Schedule, applied []Applied, ops []history.Op) *ResilienceMetrics {
	paired, err := history.Pair(ops)
	if err != nil || len(paired) == 0 {
		return nil
	}
	type window struct {
		id    int
		kind  nemesis.Kind
		start time.Duration
		end   time.Duration
	}
	windows := make([]window, 0, len(sched.Faults))
	startOverride := make(map[int]time.Duration)
	endOverride := make(map[int]time.Duration)
	for _, a := range applied {
		if len(a.Errors) == 0 && a.Landed > 0 {
			if a.Start {
				startOverride[a.FaultID] = a.Landed
			} else {
				endOverride[a.FaultID] = a.Landed
			}
		}
	}
	for _, f := range sched.Faults {
		s, e := f.Start, f.End
		if v, ok := startOverride[f.ID]; ok {
			s = v
		}
		if v, ok := endOverride[f.ID]; ok {
			e = v
		}
		windows = append(windows, window{id: f.ID, kind: f.Kind, start: s, end: e})
	}

	var totalOK, faultTotal, faultOK, healthyTotal, healthyOK int
	for _, op := range paired {
		callDur := time.Duration(op.Call)
		inFault := false
		for _, w := range windows {
			if callDur >= w.start && callDur <= w.end {
				inFault = true
				break
			}
		}
		isOK := op.Status() == history.OK
		if isOK {
			totalOK++
		}
		if inFault {
			faultTotal++
			if isOK {
				faultOK++
			}
		} else {
			healthyTotal++
			if isOK {
				healthyOK++
			}
		}
	}

	res := &ResilienceMetrics{
		OverallAvailability: float64(totalOK) / float64(len(paired)),
	}
	if healthyTotal > 0 {
		res.HealthyWindowAvailability = float64(healthyOK) / float64(healthyTotal)
	} else {
		res.HealthyWindowAvailability = res.OverallAvailability
	}
	if faultTotal > 0 {
		res.FaultWindowAvailability = float64(faultOK) / float64(faultTotal)
	}

	var mttrs []time.Duration
	for _, w := range windows {
		fr := FaultRecovery{
			FaultID:  w.id,
			Kind:     w.kind,
			HealedAt: w.end,
		}
		bestRet := int64(-1)
		for _, op := range paired {
			if op.Status() != history.OK || op.Return == history.Infinity {
				continue
			}
			if time.Duration(op.Return) >= w.end {
				if bestRet < 0 || op.Return < bestRet {
					bestRet = op.Return
				}
			}
		}
		if bestRet >= 0 {
			fr.Recovered = time.Duration(bestRet)
			fr.MTTR = fr.Recovered - w.end
			if fr.MTTR < 0 {
				fr.MTTR = 0
			}
			fr.RecoveredOK = true
			mttrs = append(mttrs, fr.MTTR)
		}
		res.Recoveries = append(res.Recoveries, fr)
	}
	res.MTTR = stats.Summarize(mttrs)
	return res
}

// Report is the complete outcome of a run.
type Report struct {
	Tool         string             `json:"tool"`
	Version      string             `json:"version"`
	Kind         string             `json:"kind"`
	Target       string             `json:"target"`
	Seed         uint64             `json:"seed"`
	Verdict      checker.Verdict    `json:"verdict"`
	Fingerprint  string             `json:"fingerprint"`
	ScheduleHash string             `json:"schedule_hash,omitempty"`
	HistoryHash  string             `json:"history_hash"`
	TraceHash    string             `json:"trace_hash,omitempty"`
	Events       uint64             `json:"events,omitempty"`
	Nodes        int                `json:"nodes,omitempty"`
	Duration     time.Duration      `json:"duration,omitempty"`
	Config       json.RawMessage    `json:"config,omitempty"`
	Schedule     nemesis.Schedule   `json:"schedule"`
	Applied      []Applied          `json:"applied,omitempty"`
	Resilience   *ResilienceMetrics `json:"resilience,omitempty"`
	Probes       []probe.Event      `json:"probes,omitempty"`
	Findings     []probe.Finding    `json:"findings,omitempty"`
	Check        checker.Report     `json:"check"`
	Reproduce    string             `json:"reproduce,omitempty"`
	GeneratedAt  string             `json:"generated_at,omitempty"`
	Shrink       *ShrinkInfo        `json:"shrink,omitempty"`
	Notes        []string           `json:"notes,omitempty"`
}

// ShrinkInfo describes how a minimal scenario was derived from a failing one.
type ShrinkInfo struct {
	OriginalFaults       int           `json:"original_faults"`
	MinimalFaults        int           `json:"minimal_faults"`
	OriginalOps          int           `json:"original_ops"`
	MinimalOps           int           `json:"minimal_ops"`
	OriginalDuration     time.Duration `json:"original_duration"`
	MinimalDuration      time.Duration `json:"minimal_duration"`
	OriginalClients      int           `json:"original_clients"`
	MinimalClients       int           `json:"minimal_clients"`
	OriginalScheduleHash string        `json:"original_schedule_hash"`
	Runs                 int           `json:"runs"`
	Elapsed              time.Duration `json:"elapsed"`
	Steps                []string      `json:"steps"`
	BudgetExhausted      bool          `json:"budget_exhausted,omitempty"`
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
		Resilience:  ComputeResilience(sched, nil, ops),
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
	if len(r.Probes) > 0 {
		steps = append(steps, struct {
			name string
			fn   func(f *os.File) error
		}{
			"probes.jsonl",
			func(f *os.File) error { return probe.WriteEvents(f, r.Probes) },
		})
	}
	for _, s := range steps {
		if err := write(s.name, s.fn); err != nil {
			return written, err
		}
	}
	return written, nil
}
