package explore

import (
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

func quick(system string, faults string) Config {
	c := DefaultConfig()
	c.System = system
	c.Duration = 6 * time.Second
	k, err := nemesis.ParseKinds(faults)
	if err != nil {
		panic(err)
	}
	c.Faults = k
	return c
}

func TestDeterminism(t *testing.T) {
	a, b, ok, err := Determinism(quick("raft", "all"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("trace %s vs %s", a.TraceHash, b.TraceHash)
	}
	c := quick("raft", "all")
	c.Seed = 2
	other, err := Run(c)
	if err != nil {
		t.Fatal(err)
	}
	if other.TraceHash == a.TraceHash {
		t.Fatal("different seeds produced identical traces")
	}
}

func TestDeterminismAcrossParallelExploration(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	a := Explore(quick("raft-stale-reads", "partition,kill"), seeds, 1, nil)
	b := Explore(quick("raft-stale-reads", "partition,kill"), seeds, 4, nil)
	for i := range a {
		if a[i].TraceHash != b[i].TraceHash || a[i].Verdict != b[i].Verdict {
			t.Fatalf("seed %d diverged between serial and parallel exploration", a[i].Seed)
		}
	}
}

func TestCorrectRaftSurvivesFaults(t *testing.T) {
	n := uint64(24)
	if testing.Short() {
		n = 6
	}
	var seeds []uint64
	for s := uint64(1); s <= n; s++ {
		seeds = append(seeds, s)
	}
	for _, s := range Explore(quick("raft", "all"), seeds, 0, nil) {
		if s.Error != "" || s.Verdict != checker.Linearizable {
			t.Errorf("seed %d: verdict %s %s", s.Seed, s.Verdict, s.Error)
		}
		if s.Ops < 500 {
			t.Errorf("seed %d: only %d ops, workload starved", s.Seed, s.Ops)
		}
	}
}

// Each buggy system must be caught somewhere in a small seed range. The
// simulator is deterministic, so these are stable regression tests for the
// engine's bug-finding power, not flaky probabilistic ones.
func TestBugsAreFound(t *testing.T) {
	if testing.Short() {
		t.Skip("explores many seeds")
	}
	for _, sys := range Systems() {
		if sys.Expect != checker.Violation {
			continue
		}
		t.Run(sys.Name, func(t *testing.T) {
			var seeds []uint64
			for s := uint64(1); s <= 60; s++ {
				seeds = append(seeds, s)
			}
			found := 0
			for _, s := range Explore(quick(sys.Name, sys.Hint), seeds, 0, nil) {
				if s.Verdict == checker.Violation {
					found++
				}
			}
			if found == 0 {
				t.Fatalf("no violation found in %d seeds with faults %s", len(seeds), sys.Hint)
			}
			t.Logf("%d/%d seeds violate", found, len(seeds))
		})
	}
}

func TestPlanOverride(t *testing.T) {
	c := quick("raft", "none")
	c.Plan = &nemesis.Schedule{Nodes: c.Nodes, Duration: c.Duration, Faults: []nemesis.Fault{
		{ID: 0, Kind: nemesis.Kill, Start: time.Second, End: 2 * time.Second, Nodes: []int{0, 1, 2, 3, 4}},
	}}
	res, err := Run(c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Check.Verdict != checker.Linearizable || len(res.Schedule.Faults) != 1 {
		t.Fatalf("got %s with %d faults", res.Check.Verdict, len(res.Schedule.Faults))
	}
	if res.Check.Stats.Info == 0 {
		t.Fatal("killing the whole cluster should leave some operations indeterminate")
	}
	c.Plan.Faults[0].Nodes = []int{9}
	if _, err := Run(c); err == nil {
		t.Fatal("expected out-of-range node error")
	}
}

func TestParseSeeds(t *testing.T) {
	s, err := ParseSeeds("1..3, 10")
	if err != nil || len(s) != 4 || s[3] != 10 {
		t.Fatalf("got %v %v", s, err)
	}
	for _, bad := range []string{"x", "5..1", "1..x"} {
		if _, err := ParseSeeds(bad); err == nil {
			t.Fatalf("%q should fail", bad)
		}
	}
}
