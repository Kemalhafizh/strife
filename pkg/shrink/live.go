package shrink

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// LiveOptions configure shrinking against a real cluster.
//
// A live cluster is not deterministic: the same schedule may or may not
// trigger a bug. Each candidate schedule is therefore run up to Attempts
// times and counts as failing if any attempt fails, and the result is a
// candidate reproducer with a measured reproduction rate, never a proof of
// minimality.
type LiveOptions struct {
	// Run executes one live test with the given schedule.
	Run func(ctx context.Context, sched nemesis.Schedule) (checker.Verdict, error)
	// Attempts per candidate (default 3).
	Attempts int
	// MaxRuns bounds the total number of live runs (default 40).
	MaxRuns int
	// Verify is how many times the minimal schedule is re-run to measure
	// its reproduction rate (default 5).
	Verify int
	Logf   func(format string, args ...any)
}

// LiveResult is the outcome of a live shrink.
type LiveResult struct {
	Original nemesis.Schedule `json:"original"`
	Minimal  nemesis.Schedule `json:"minimal"`
	// Runs is the number of live runs spent, Verified/Reproduced the
	// verification runs and how many of them failed.
	Runs       int           `json:"runs"`
	Verified   int           `json:"verified"`
	Reproduced int           `json:"reproduced"`
	Steps      []string      `json:"steps"`
	Exhausted  bool          `json:"budget_exhausted,omitempty"`
	Elapsed    time.Duration `json:"elapsed"`
}

// Rate is the fraction of verification runs that reproduced a violation.
func (r *LiveResult) Rate() float64 {
	if r.Verified == 0 {
		return 0
	}
	return float64(r.Reproduced) / float64(r.Verified)
}

// ErrNotReproduced means the original schedule did not fail in any attempt.
var ErrNotReproduced = errors.New("shrink: the original schedule did not reproduce a violation")

type liveShrinker struct {
	ctx  context.Context
	o    LiveOptions
	res  *LiveResult
	memo map[string]bool
}

// Live shrinks sched's faults with delta debugging against a real cluster.
func Live(ctx context.Context, sched nemesis.Schedule, o LiveOptions) (*LiveResult, error) {
	if o.Run == nil {
		return nil, errors.New("shrink: no run function")
	}
	if o.Attempts <= 0 {
		o.Attempts = 3
	}
	if o.MaxRuns <= 0 {
		o.MaxRuns = 40
	}
	if o.Verify <= 0 {
		o.Verify = 5
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	start := time.Now()
	s := &liveShrinker{ctx: ctx, o: o, res: &LiveResult{Original: sched}, memo: map[string]bool{}}
	if !s.fails(sched.Faults) {
		s.res.Elapsed = time.Since(start)
		return s.res, ErrNotReproduced
	}
	min := sched.Faults
	if s.fails(nil) {
		s.step("the violation occurs without any fault: the faults are not needed to reproduce it")
		min = nil
	} else {
		min = s.ddmin(sched.Faults)
	}
	s.res.Minimal = with(sched, min)
	for i := 0; i < o.Verify && ctx.Err() == nil; i++ {
		v, err := o.Run(ctx, s.res.Minimal)
		if err != nil {
			return s.res, err
		}
		s.res.Verified++
		if v == checker.Violation {
			s.res.Reproduced++
		}
		o.Logf("verify %d/%d: %s", i+1, o.Verify, v)
	}
	s.step(fmt.Sprintf("minimal candidate: %d of %d faults, reproduced in %d of %d verification runs",
		len(min), len(sched.Faults), s.res.Reproduced, s.res.Verified))
	s.res.Elapsed = time.Since(start)
	return s.res, ctx.Err()
}

func with(sched nemesis.Schedule, faults []nemesis.Fault) nemesis.Schedule {
	out := sched
	out.Faults = append([]nemesis.Fault(nil), faults...)
	return out
}

func (s *liveShrinker) step(msg string) {
	s.res.Steps = append(s.res.Steps, msg)
	s.o.Logf("%s", msg)
}

func liveKey(fs []nemesis.Fault) string {
	ids := make([]int, len(fs))
	for i, f := range fs {
		ids[i] = f.ID
	}
	sort.Ints(ids)
	return fmt.Sprint(ids)
}

// fails runs the candidate until it fails or attempts run out.
func (s *liveShrinker) fails(faults []nemesis.Fault) bool {
	k := liveKey(faults)
	if v, ok := s.memo[k]; ok {
		return v
	}
	for a := 0; a < s.o.Attempts; a++ {
		if s.res.Runs >= s.o.MaxRuns || s.ctx.Err() != nil {
			s.res.Exhausted = true
			return false
		}
		s.res.Runs++
		v, err := s.o.Run(s.ctx, with(s.res.Original, faults))
		s.o.Logf("run %d: %d faults %s → %s", s.res.Runs, len(faults), k, v)
		if err == nil && v == checker.Violation {
			s.memo[k] = true
			return true
		}
	}
	s.memo[k] = false
	return false
}

// ddmin is Zeller's delta debugging over the faults.
func (s *liveShrinker) ddmin(fs []nemesis.Fault) []nemesis.Fault {
	n := 2
	for len(fs) >= 2 {
		parts := split(fs, n)
		reduced := false
		for _, p := range parts {
			if s.fails(p) {
				s.step(fmt.Sprintf("kept %d of %d faults (a subset still fails)", len(p), len(fs)))
				fs, n, reduced = p, 2, true
				break
			}
		}
		if !reduced {
			for _, p := range parts {
				c := complement(fs, p)
				if s.fails(c) {
					s.step(fmt.Sprintf("dropped %d of %d faults", len(fs)-len(c), len(fs)))
					fs, n, reduced = c, max(n-1, 2), true
					break
				}
			}
		}
		if !reduced {
			if n >= len(fs) {
				break
			}
			n = min(2*n, len(fs))
		}
		if s.res.Exhausted {
			break
		}
	}
	if len(fs) == 1 && !s.res.Exhausted && s.fails(nil) {
		fs = nil
	}
	return fs
}
