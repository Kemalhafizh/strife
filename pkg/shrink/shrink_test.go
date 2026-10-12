package shrink

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/explore"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// oracle is a fake simulator with a planted bug: it "violates" iff
//   - fault 2 (kill) is present and still targets node 1,
//   - fault 5 (partition) is present and is active at t=3s,
//   - the test runs for at least 2s with at least 3 clients.
//
// Everything else in the schedule is noise the shrinker must remove.
type oracle struct{ runs atomic.Int64 }

func generated(cfg explore.Config) nemesis.Schedule {
	s := nemesis.Schedule{Seed: cfg.Seed, Nodes: cfg.Nodes, Duration: cfg.Duration}
	for i := 0; i < 9; i++ {
		f := nemesis.Fault{ID: i, Kind: nemesis.Latency, Start: time.Duration(i) * time.Second, End: time.Duration(i)*time.Second + 1500*time.Millisecond,
			Nodes: []int{0, 1, 2}, Delay: 200 * time.Millisecond, Jitter: 50 * time.Millisecond}
		switch i {
		case 2:
			f.Kind, f.Delay, f.Jitter = nemesis.Kill, 0, 0
			f.Start, f.End = 1234567890, 2*time.Second+987654321
		case 5:
			f.Kind, f.Delay, f.Jitter = nemesis.Partition, 0, 0
			f.Start, f.End = 2*time.Second+111111111, 4*time.Second+222222222
			f.Nodes = []int{0, 3}
		}
		s.Faults = append(s.Faults, f)
	}
	return s
}

func (o *oracle) run(cfg explore.Config) (*explore.Result, error) {
	o.runs.Add(1)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	sched := generated(cfg)
	if cfg.Plan != nil {
		sched = *cfg.Plan
	}
	bad := cfg.Duration >= 2*time.Second && cfg.Clients >= 3
	var kill, part bool
	for _, f := range sched.Faults {
		if f.ID == 2 && f.Kind == nemesis.Kill && contains(f.Nodes, 1) {
			kill = true
		}
		if f.ID == 5 && f.Start <= 3*time.Second && f.End > 3*time.Second {
			part = true
		}
	}
	v := checker.Linearizable
	if bad && kill && part {
		v = checker.Violation
	}
	return &explore.Result{Config: cfg, Schedule: sched, Check: checker.Report{Verdict: v, Operations: int(cfg.Duration / time.Millisecond)}}, nil
}

func contains(xs []int, x int) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func base() explore.Config {
	c := explore.DefaultConfig()
	c.Seed = 42
	return c
}

func TestShrinkFindsPlantedBug(t *testing.T) {
	o := &oracle{}
	res, err := Shrink(context.Background(), base(), Options{Run: o.run})
	if err != nil {
		t.Fatal(err)
	}
	fs := res.Faults()
	ids := []int{}
	for _, f := range fs {
		ids = append(ids, f.ID)
	}
	sort.Ints(ids)
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 5 {
		t.Fatalf("want faults [2 5], got %v", ids)
	}
	for _, f := range fs {
		switch f.ID {
		case 2:
			if len(f.Nodes) != 1 || f.Nodes[0] != 1 {
				t.Errorf("kill should be narrowed to node 1, got %v", f.Nodes)
			}
		case 5:
			if len(f.Nodes) != 1 {
				t.Errorf("partition side should shrink to one node, got %v", f.Nodes)
			}
			if !(f.Start <= 3*time.Second && f.End > 3*time.Second) {
				t.Errorf("partition window must still cover 3s: %v–%v", f.Start, f.End)
			}
			// From 2.1s down to the instant that matters, widened by at most
			// one 100ms grain on each side when snapped to the grid.
			if f.End-f.Start > 250*time.Millisecond {
				t.Errorf("partition window not shrunk: %v", f.End-f.Start)
			}
			if f.Start%(100*time.Millisecond) != 0 && f.Start%(10*time.Millisecond) != 0 {
				t.Errorf("start %v not snapped to a grid", f.Start)
			}
		}
	}
	m := res.Minimal
	if m.Duration < 2*time.Second || m.Duration > 2*time.Second+20*time.Millisecond {
		t.Errorf("duration should shrink to ~2s, got %v", m.Duration)
	}
	if m.Clients != 3 {
		t.Errorf("clients should shrink to 3, got %d", m.Clients)
	}
	if res.BudgetExhausted || res.Runs != int(o.runs.Load()) {
		t.Errorf("runs accounting: result says %d, oracle saw %d (exhausted=%v)", res.Runs, o.runs.Load(), res.BudgetExhausted)
	}
	if res.Final.Check.Verdict != checker.Violation {
		t.Fatal("final scenario must be re-proven to violate")
	}
	last := res.Steps[len(res.Steps)-1]
	if last.Phase != "verify" || last.Faults != 2 {
		t.Fatalf("unexpected final step %+v", last)
	}
}

func TestShrinkIsOneMinimal(t *testing.T) {
	o := &oracle{}
	res, err := Shrink(context.Background(), base(), Options{Run: o.run})
	if err != nil {
		t.Fatal(err)
	}
	fs := res.Faults()
	for i := range fs {
		c := res.Minimal
		p := *c.Plan
		p.Faults = append(append([]nemesis.Fault(nil), fs[:i]...), fs[i+1:]...)
		c.Plan = &p
		r, _ := o.run(c)
		if r.Check.Verdict == checker.Violation {
			t.Fatalf("fault %d is not necessary", fs[i].ID)
		}
	}
}

func TestShrinkIsDeterministic(t *testing.T) {
	enc := func() string {
		res, err := Shrink(context.Background(), base(), Options{Run: (&oracle{}).run, Workers: 3})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(res.Minimal)
		return string(b)
	}
	a := enc()
	for i := 0; i < 3; i++ {
		if b := enc(); b != a {
			t.Fatalf("shrinking is not deterministic:\n%s\n%s", a, b)
		}
	}
	// ...and independent of parallelism.
	res, err := Shrink(context.Background(), base(), Options{Run: (&oracle{}).run, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res.Minimal); string(b) != a {
		t.Fatal("result depends on worker count")
	}
}

func TestShrinkRejectsPassingScenario(t *testing.T) {
	c := base()
	c.Clients = 2 // the planted bug needs 3
	_, err := Shrink(context.Background(), c, Options{Run: (&oracle{}).run})
	if !errors.Is(err, ErrNotViolating) {
		t.Fatalf("want ErrNotViolating, got %v", err)
	}
}

func TestShrinkRespectsBudget(t *testing.T) {
	o := &oracle{}
	res, err := Shrink(context.Background(), base(), Options{Run: o.run, MaxRuns: 12})
	if err != nil {
		t.Fatal(err)
	}
	if !res.BudgetExhausted {
		t.Fatal("budget should be reported as exhausted")
	}
	// baseline + at most MaxRuns-1 candidates + the final re-proof.
	if got := o.runs.Load(); got > 13 {
		t.Fatalf("ran %d simulations with a budget of 12", got)
	}
	if res.Final.Check.Verdict != checker.Violation {
		t.Fatal("a partial result must still violate")
	}
}

func TestKeepWorkload(t *testing.T) {
	res, err := Shrink(context.Background(), base(), Options{Run: (&oracle{}).run, KeepWorkload: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Minimal.Duration != base().Duration || res.Minimal.Clients != base().Clients {
		t.Fatalf("workload changed: %v, %d clients", res.Minimal.Duration, res.Minimal.Clients)
	}
	if len(res.Faults()) != 2 {
		t.Fatalf("faults should still shrink, got %d", len(res.Faults()))
	}
}

func TestSplit(t *testing.T) {
	fs := make([]nemesis.Fault, 7)
	for i := range fs {
		fs[i].ID = i
	}
	parts := split(fs, 3)
	if len(parts) != 3 || len(parts[0]) != 3 || len(parts[1]) != 2 || len(parts[2]) != 2 {
		t.Fatalf("bad split: %v", parts)
	}
	if c := complement(fs, parts[1]); len(c) != 5 || c[3].ID != 5 {
		t.Fatalf("bad complement: %v", c)
	}
	if got := split(fs[:2], 5); len(got) != 2 {
		t.Fatalf("more chunks than faults: %v", got)
	}
}

// TestShrinkRealBug runs the real simulator: Raft serving stale reads under
// partitions must shrink to a scenario of at most three faults whose replay
// is bit-for-bit identical.
func TestShrinkRealBug(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the simulator dozens of times")
	}
	cfg := explore.DefaultConfig()
	cfg.System = "raft-stale-reads"
	cfg.Faults = []nemesis.Kind{nemesis.Partition}
	cfg.Seed = 2
	res, err := Shrink(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(res.Faults()); n > 3 || n >= len(res.OriginalSchedule.Faults) {
		t.Fatalf("shrunk %d → %d faults", len(res.OriginalSchedule.Faults), n)
	}
	again, err := explore.Run(res.Minimal)
	if err != nil {
		t.Fatal(err)
	}
	if again.Check.Verdict != checker.Violation || again.TraceHash != res.Final.TraceHash {
		t.Fatalf("minimal scenario does not replay: %s %s vs %s", again.Check.Verdict, again.TraceHash, res.Final.TraceHash)
	}
}
