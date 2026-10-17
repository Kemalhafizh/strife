package shrink

import (
	"context"
	"testing"

	"github.com/kemalhafizh/strife/internal/rng"
	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// flaky simulates a cluster where the bug needs faults 2 and 5 together and
// then shows up only with probability p per run.
func flaky(p float64, seed uint64) func(context.Context, nemesis.Schedule) (checker.Verdict, error) {
	r := rng.New(seed)
	return func(_ context.Context, s nemesis.Schedule) (checker.Verdict, error) {
		has := map[int]bool{}
		for _, f := range s.Faults {
			has[f.ID] = true
		}
		if has[2] && has[5] && r.Bool(p) {
			return checker.Violation, nil
		}
		return checker.Linearizable, nil
	}
}

func schedule(n int) nemesis.Schedule {
	s := nemesis.Schedule{Nodes: 3}
	for i := 0; i < n; i++ {
		s.Faults = append(s.Faults, nemesis.Fault{ID: i, Kind: nemesis.Partition, Nodes: []int{0}})
	}
	return s
}

func TestLiveShrinkFindsTheEssentialFaults(t *testing.T) {
	res, err := Live(context.Background(), schedule(8), LiveOptions{Run: flaky(1, 1), Attempts: 1, MaxRuns: 100, Verify: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Minimal.Faults) != 2 || res.Minimal.Faults[0].ID != 2 || res.Minimal.Faults[1].ID != 5 {
		t.Fatalf("minimal %v", res.Minimal.Faults)
	}
	if res.Reproduced != 4 || res.Rate() != 1 {
		t.Fatalf("verification %d/%d", res.Reproduced, res.Verified)
	}
}

func TestLiveShrinkWithFlakyReproduction(t *testing.T) {
	res, err := Live(context.Background(), schedule(8), LiveOptions{Run: flaky(0.6, 3), Attempts: 4, MaxRuns: 200, Verify: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Minimal.Faults) > 3 {
		t.Fatalf("not shrunk: %v", res.Minimal.Faults)
	}
	if res.Rate() <= 0.2 || res.Rate() >= 1 {
		t.Fatalf("rate %.2f should reflect the 60%% flakiness", res.Rate())
	}
}

func TestLiveShrinkBudgetAndNotReproduced(t *testing.T) {
	never := func(context.Context, nemesis.Schedule) (checker.Verdict, error) { return checker.Linearizable, nil }
	if _, err := Live(context.Background(), schedule(4), LiveOptions{Run: never, Attempts: 2}); err != ErrNotReproduced {
		t.Fatalf("got %v", err)
	}
	res, err := Live(context.Background(), schedule(30), LiveOptions{Run: flaky(1, 1), Attempts: 1, MaxRuns: 5, Verify: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exhausted || res.Runs > 5 {
		t.Fatalf("budget not respected: %+v", res)
	}
	// Without any fault the violation persists: nothing is needed.
	always := func(context.Context, nemesis.Schedule) (checker.Verdict, error) { return checker.Violation, nil }
	res, _ = Live(context.Background(), schedule(4), LiveOptions{Run: always, Attempts: 1, Verify: 1})
	if len(res.Minimal.Faults) != 0 {
		t.Fatalf("faults not needed but kept: %v", res.Minimal.Faults)
	}
}
