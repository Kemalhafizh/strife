// Package explore runs systems inside the deterministic simulator under
// generated fault schedules, checks the resulting histories, and searches
// many seeds for violations.
package explore

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/internal/rng"
	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/sim"
	"github.com/kemalhafizh/strife/pkg/sim/raft"
)

// System is a simulated system under test.
type System struct {
	Name        string
	Description string
	Raft        raft.Config
	// Expect is what Strife should conclude about it under a broad fault mix.
	Expect checker.Verdict
	// Hint names the faults that expose the bug, if any.
	Hint string
}

// Systems lists the built-in simulated systems.
func Systems() []System {
	d := raft.Defaults()
	stale, lease, unsafe := d, d, d
	stale.StaleReads = true
	lease.LeaseReads = true
	unsafe.UnsafeFsync = true
	return []System{
		{Name: "raft", Description: "Correct Raft; reads go through the log.", Raft: d, Expect: checker.Linearizable},
		{Name: "raft-stale-reads", Description: "Leader serves reads locally without confirming leadership.", Raft: stale, Expect: checker.Violation, Hint: "partition"},
		{Name: "raft-lease", Description: "Leader-lease reads timed with the wall clock; clock jumps outlive the lease.", Raft: lease, Expect: checker.Violation, Hint: "clock-skew,partition"},
		{Name: "raft-unsafe-fsync", Description: "Acks votes and entries before fsync completes.", Raft: unsafe, Expect: checker.Violation, Hint: "kill,io-stall"},
	}
}

// LookupSystem finds a built-in system by name.
func LookupSystem(name string) (System, error) {
	var names []string
	for _, s := range Systems() {
		if s.Name == name {
			return s, nil
		}
		names = append(names, s.Name)
	}
	return System{}, fmt.Errorf("unknown system %q (available: %s)", name, strings.Join(names, ", "))
}

// Config describes one simulated test.
type Config struct {
	System        string         `json:"system"`
	Seed          uint64         `json:"seed"`
	Nodes         int            `json:"nodes"`
	Clients       int            `json:"clients"`
	Keys          int            `json:"keys"`
	Values        int            `json:"values"`
	Duration      time.Duration  `json:"duration"`
	ReadRatio     float64        `json:"read_ratio"`
	CasRatio      float64        `json:"cas_ratio"`
	OpTimeout     time.Duration  `json:"op_timeout"`
	Think         time.Duration  `json:"think"`
	Faults        []nemesis.Kind `json:"faults"`
	FaultInterval time.Duration  `json:"fault_interval"`
	FaultLength   time.Duration  `json:"fault_length"`
	MaxSteps      uint64         `json:"max_steps,omitempty"`
	// Plan, if set, replaces the generated fault schedule (replaying a
	// hand-written or previously exported scenario).
	Plan *nemesis.Schedule `json:"plan,omitempty"`
}

// DefaultConfig is a 5-node cluster under every fault kind for 10 simulated
// seconds.
func DefaultConfig() Config {
	return Config{
		System:        "raft",
		Seed:          1,
		Nodes:         5,
		Clients:       10,
		Keys:          4,
		Values:        5,
		Duration:      10 * time.Second,
		ReadRatio:     0.5,
		CasRatio:      0.2,
		OpTimeout:     500 * time.Millisecond,
		Think:         20 * time.Millisecond,
		Faults:        append([]nemesis.Kind(nil), nemesis.AllKinds...),
		FaultInterval: time.Second,
		FaultLength:   1500 * time.Millisecond,
	}
}

// Validate checks a config for obvious mistakes.
func (c Config) Validate() error {
	if _, err := LookupSystem(c.System); err != nil {
		return err
	}
	switch {
	case c.Nodes < 1 || c.Nodes > 99:
		return fmt.Errorf("nodes must be in [1, 99]")
	case c.Clients < 1 || c.Clients > 500:
		return fmt.Errorf("clients must be in [1, 500]")
	case c.Keys < 1, c.Values < 1:
		return fmt.Errorf("keys and values must be positive")
	case c.Duration <= 0, c.OpTimeout <= 0:
		return fmt.Errorf("duration and op timeout must be positive")
	case c.ReadRatio < 0 || c.CasRatio < 0 || c.ReadRatio+c.CasRatio > 1:
		return fmt.Errorf("read_ratio + cas_ratio must be within [0, 1]")
	}
	return nil
}

// Result is the outcome of one simulated test.
type Result struct {
	Config       Config           `json:"config"`
	Schedule     nemesis.Schedule `json:"schedule"`
	History      []history.Op     `json:"-"`
	Check        checker.Report   `json:"check"`
	TraceHash    string           `json:"trace_hash"`
	HistoryHash  string           `json:"history_hash"`
	ScheduleHash string           `json:"schedule_hash"`
	Events       uint64           `json:"events"`
	Elapsed      time.Duration    `json:"-"` // wall clock; not part of any hash
}

// Run executes one deterministic simulation and checks it.
func Run(cfg Config) (*Result, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	start := time.Now()
	sys, _ := LookupSystem(cfg.System)
	root := rng.New(cfg.Seed)
	w := sim.NewWorld(sim.Config{
		Seed:        cfg.Seed,
		MinLatency:  500 * time.Microsecond,
		MaxLatency:  5 * time.Millisecond,
		DiskLatency: 2 * time.Millisecond,
	})
	for i := 0; i < cfg.Nodes; i++ {
		id := sim.NodeID(i)
		w.Add(id, raft.New(w, id, cfg.Nodes, sys.Raft, root.Fork(fmt.Sprintf("raft/n%d", i))))
	}
	rec := history.NewRecorder(w.Now)
	for i := 0; i < cfg.Clients; i++ {
		id := sim.ClientBase + sim.NodeID(i)
		w.Add(id, &client{
			w: w, id: id, idx: i, cfg: cfg, rec: rec, process: i,
			rand:  root.Fork(fmt.Sprintf("client/%d", i)),
			guess: sim.NodeID(i % cfg.Nodes),
		})
	}

	sched := nemesis.Generate(cfg.Seed, nemesis.Options{
		Nodes: cfg.Nodes, Duration: cfg.Duration, Kinds: cfg.Faults,
		Interval: cfg.FaultInterval, Length: cfg.FaultLength,
	})
	if cfg.Plan != nil {
		sched = *cfg.Plan
		for _, f := range sched.Faults {
			for _, n := range f.Nodes {
				if n < 0 || n >= cfg.Nodes {
					return nil, fmt.Errorf("plan: fault %d targets node %d of a %d-node cluster", f.ID, n, cfg.Nodes)
				}
			}
		}
	}
	for _, a := range sched.Timeline() {
		a := a
		w.At(int64(a.At), func() { applyFault(w, a, cfg.Nodes) })
	}

	w.Start()
	w.Run(int64(cfg.Duration + cfg.OpTimeout + 100*time.Millisecond))

	ops := rec.Ops()
	rep, err := checker.Check(model.KV{}, ops, checker.Options{MaxSteps: cfg.MaxSteps})
	if err != nil {
		return nil, err
	}
	return &Result{
		Config:       cfg,
		Schedule:     sched,
		History:      ops,
		Check:        rep,
		TraceHash:    w.TraceHash(),
		HistoryHash:  history.Hash(ops),
		ScheduleHash: sched.Hash(),
		Events:       w.Events(),
		Elapsed:      time.Since(start),
	}, nil
}

func ids(xs []int) []sim.NodeID {
	out := make([]sim.NodeID, len(xs))
	for i, x := range xs {
		out[i] = sim.NodeID(x)
	}
	return out
}

func applyFault(w *sim.World, a nemesis.Action, n int) {
	f := a.Fault
	targets := ids(f.Nodes)
	switch f.Kind {
	case nemesis.Partition:
		other := ids(f.Other(n))
		if a.Start {
			w.Partition(targets, other)
		} else {
			w.Heal(targets, other)
		}
		return
	}
	for _, id := range targets {
		switch f.Kind {
		case nemesis.Kill:
			if a.Start {
				w.Crash(id)
			} else {
				w.Restart(id)
			}
		case nemesis.Pause, nemesis.Freeze:
			if a.Start {
				w.Freeze(id)
			} else {
				w.Thaw(id)
			}
		case nemesis.IOStall:
			if a.Start {
				w.StallIO(id)
			} else {
				w.ResumeIO(id)
			}
		case nemesis.Latency:
			if a.Start {
				w.SetLatency(id, f.Delay, f.Jitter)
			} else {
				w.SetLatency(id, 0, 0)
			}
		case nemesis.Loss:
			if a.Start {
				w.SetLoss(id, f.Loss)
			} else {
				w.SetLoss(id, 0)
			}
		case nemesis.ClockSkew:
			if a.Start {
				w.SetClockRate(id, f.Rate)
				w.JumpClock(id, f.Offset)
			} else {
				w.SetClockRate(id, 1)
				w.JumpClock(id, -f.Offset)
			}
		}
	}
}

// Determinism runs the same config twice and reports whether both executions
// produced identical trace and history hashes.
func Determinism(cfg Config) (first, second *Result, ok bool, err error) {
	if first, err = Run(cfg); err != nil {
		return nil, nil, false, err
	}
	if second, err = Run(cfg); err != nil {
		return nil, nil, false, err
	}
	ok = first.TraceHash == second.TraceHash && first.HistoryHash == second.HistoryHash
	return first, second, ok, nil
}

// Summary is the compact outcome of one seed.
type Summary struct {
	Seed      uint64          `json:"seed"`
	Verdict   checker.Verdict `json:"verdict"`
	Ops       int             `json:"ops"`
	Faults    int             `json:"faults"`
	TraceHash string          `json:"trace_hash"`
	Error     string          `json:"error,omitempty"`
}

// Explore runs base with every seed in seeds using up to workers goroutines.
// onResult, if set, is called (serialized) with every full result as it
// completes, so callers can keep the interesting ones. Summaries are returned
// in seed order.
func Explore(base Config, seeds []uint64, workers int, onResult func(*Result)) []Summary {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	out := make([]Summary, len(seeds))
	jobs := make(chan int)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				cfg := base
				cfg.Seed = seeds[j]
				res, err := Run(cfg)
				s := Summary{Seed: seeds[j]}
				if err != nil {
					s.Error = err.Error()
				} else {
					s.Verdict, s.Ops, s.Faults, s.TraceHash = res.Check.Verdict, res.Check.Operations, len(res.Schedule.Faults), res.TraceHash
				}
				mu.Lock()
				out[j] = s
				if onResult != nil && res != nil {
					onResult(res)
				}
				mu.Unlock()
			}
		}()
	}
	for j := range seeds {
		jobs <- j
	}
	close(jobs)
	wg.Wait()
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seed < out[j].Seed })
	return out
}

// ParseSeeds accepts "7", "1..100" or "3,9,27".
func ParseSeeds(s string) ([]uint64, error) {
	var out []uint64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if lo, hi, ok := strings.Cut(part, ".."); ok {
			var a, b uint64
			if _, err := fmt.Sscan(lo, &a); err != nil {
				return nil, fmt.Errorf("bad seed range %q", part)
			}
			if _, err := fmt.Sscan(hi, &b); err != nil || b < a {
				return nil, fmt.Errorf("bad seed range %q", part)
			}
			if b-a > 10_000_000 {
				return nil, fmt.Errorf("seed range %q too large", part)
			}
			for x := a; x <= b; x++ {
				out = append(out, x)
			}
			continue
		}
		var x uint64
		if _, err := fmt.Sscan(part, &x); err != nil {
			return nil, fmt.Errorf("bad seed %q", part)
		}
		out = append(out, x)
	}
	return out, nil
}
