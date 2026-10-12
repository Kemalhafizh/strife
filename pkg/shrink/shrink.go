// Package shrink reduces a failing simulation to a minimal scenario that
// still violates linearizability.
//
// Because the simulator is deterministic, "does this smaller scenario still
// fail?" is a well-defined question with a reproducible answer, so classic
// delta debugging applies. Shrinking runs in phases:
//
//  1. duration  – cut the test as short as possible (also makes every later
//     run faster);
//  2. faults    – ddmin (Zeller & Hildebrandt) over the fault schedule;
//  3. targets   – drop nodes from each remaining fault;
//  4. windows   – shorten each fault from both ends, then round fault
//     times to a human friendly grid;
//  5. params    – soften magnitudes (latency, loss, clock rate and jump);
//  6. clients   – use as few client processes as possible;
//  7. duration  – a second cut, now that the scenario is smaller;
//  8. verify    – remove any fault that became unnecessary, so the result is
//     1-minimal: dropping any single remaining fault makes the violation
//     disappear.
//
// Candidates are evaluated in parallel but always chosen in a fixed order,
// so shrinking is itself deterministic: the same input yields the same
// minimal scenario.
//
// Caveat: removing a fault changes everything downstream of it (messages,
// timers, random draws), so the minimal scenario reproduces *a* violation of
// the same system, not necessarily the identical anomaly. Reports say so.
package shrink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/explore"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// ErrNotViolating is returned when the input scenario does not fail, so there
// is nothing to shrink.
var ErrNotViolating = errors.New("shrink: the scenario does not violate linearizability")

// Options tune the search.
type Options struct {
	// MaxRuns bounds the number of simulations; 0 means 600.
	MaxRuns int
	// Workers bounds parallel simulations; 0 means GOMAXPROCS.
	Workers int
	// KeepWorkload skips the duration and client phases, shrinking only the
	// fault schedule.
	KeepWorkload bool
	// Run executes one simulation; nil means explore.Run. Tests substitute an
	// oracle.
	Run func(explore.Config) (*explore.Result, error)
	// Progress, if set, is called after every phase.
	Progress func(Step)
}

// Step records the state after one phase.
type Step struct {
	Phase    string        `json:"phase"`
	Note     string        `json:"note"`
	Faults   int           `json:"faults"`
	Duration time.Duration `json:"duration"`
	Clients  int           `json:"clients"`
	Runs     int           `json:"runs"` // cumulative
}

// Result is the outcome of shrinking.
type Result struct {
	Original         explore.Config   `json:"original"`
	OriginalSchedule nemesis.Schedule `json:"original_schedule"`
	OriginalOps      int              `json:"original_ops"`
	Minimal          explore.Config   `json:"minimal"`
	Final            *explore.Result  `json:"-"`
	Runs             int              `json:"runs"`
	Steps            []Step           `json:"steps"`
	BudgetExhausted  bool             `json:"budget_exhausted"`
	Elapsed          time.Duration    `json:"elapsed"`
}

// Faults returns the faults of the minimal scenario.
func (r *Result) Faults() []nemesis.Fault { return r.Minimal.Plan.Faults }

type shrinker struct {
	ctx     context.Context
	opts    Options
	cur     explore.Config
	runs    int
	out     *Result
	exhaust bool

	mu    sync.Mutex
	cache map[string]bool
}

// Shrink minimises cfg, which must violate linearizability.
func Shrink(ctx context.Context, cfg explore.Config, opts Options) (*Result, error) {
	start := time.Now()
	if opts.MaxRuns <= 0 {
		opts.MaxRuns = 600
	}
	if opts.Workers <= 0 {
		opts.Workers = runtime.GOMAXPROCS(0)
	}
	if opts.Run == nil {
		opts.Run = explore.Run
	}
	s := &shrinker{ctx: ctx, opts: opts, cache: map[string]bool{}, out: &Result{Original: cfg}}

	// Baseline: pin the generated schedule as an explicit plan. Running with
	// the plan is the very same execution as generating it.
	base, err := opts.Run(cfg)
	if err != nil {
		return nil, err
	}
	s.runs++
	if base.Check.Verdict != checker.Violation {
		return nil, fmt.Errorf("%w (verdict: %s)", ErrNotViolating, base.Check.Verdict)
	}
	s.out.OriginalSchedule = base.Schedule
	s.out.OriginalOps = base.Check.Operations
	s.cur = cfg
	plan := clonePlan(base.Schedule)
	s.cur.Plan = &plan
	s.cache[s.key(s.cur)] = true
	s.step("baseline", fmt.Sprintf("%d faults, %d ops", len(plan.Faults), base.Check.Operations))

	if !opts.KeepWorkload {
		s.shrinkDuration("duration")
	}
	s.ddmin()
	s.shrinkTargets()
	s.shrinkWindows()
	s.roundTimes()
	s.shrinkParams()
	if !opts.KeepWorkload {
		s.shrinkClients()
		s.shrinkDuration("duration")
	}
	s.verify()

	final, err := opts.Run(s.cur)
	if err != nil {
		return nil, err
	}
	s.runs++
	if final.Check.Verdict != checker.Violation {
		// Cannot happen with a deterministic runner; guard anyway so a
		// "minimal" scenario is never reported without being re-proven.
		return nil, fmt.Errorf("shrink: minimal scenario no longer violates (nondeterministic runner?)")
	}
	s.out.Minimal = s.cur
	s.out.Final = final
	s.out.Runs = s.runs
	s.out.BudgetExhausted = s.exhaust
	s.out.Elapsed = time.Since(start)
	return s.out, ctx.Err()
}

func (s *shrinker) step(phase, note string) {
	st := Step{Phase: phase, Note: note, Faults: len(s.cur.Plan.Faults), Duration: s.cur.Duration, Clients: s.cur.Clients, Runs: s.runs}
	s.out.Steps = append(s.out.Steps, st)
	if s.opts.Progress != nil {
		s.opts.Progress(st)
	}
}

func clonePlan(p nemesis.Schedule) nemesis.Schedule {
	c := p
	c.Faults = make([]nemesis.Fault, len(p.Faults))
	for i, f := range p.Faults {
		f.Nodes = append([]int(nil), f.Nodes...)
		c.Faults[i] = f
	}
	return c
}

// with returns a copy of the current config with faults replaced.
func (s *shrinker) with(faults []nemesis.Fault) explore.Config {
	c := s.cur
	p := clonePlan(*s.cur.Plan)
	p.Faults = make([]nemesis.Fault, len(faults))
	for i, f := range faults {
		f.Nodes = append([]int(nil), f.Nodes...)
		p.Faults[i] = f
	}
	c.Plan = &p
	return c
}

func (s *shrinker) key(c explore.Config) string {
	b, _ := json.Marshal(struct {
		D time.Duration
		C int
		F []nemesis.Fault
	}{c.Duration, c.Clients, c.Plan.Faults})
	return string(b)
}

// first evaluates candidates (in parallel) and returns the index of the
// first one, in order, that still violates, or -1. It never spends more than
// the remaining run budget.
func (s *shrinker) first(cands []explore.Config) int {
	if len(cands) == 0 || s.ctx.Err() != nil {
		return -1
	}
	results := make([]int, len(cands)) // 0 unknown, 1 violates, -1 passes
	alias := make([]int, len(cands))   // duplicate candidates share a run
	var todo []int
	seen := map[string]int{}
	s.mu.Lock()
	for i, c := range cands {
		alias[i] = i
		c.Plan.Duration = c.Duration
		cands[i] = c
		k := s.key(c)
		if v, ok := s.cache[k]; ok {
			results[i] = boolResult(v)
			continue
		}
		if j, dup := seen[k]; dup {
			alias[i] = j
			continue
		}
		seen[k] = i
		todo = append(todo, i)
	}
	s.mu.Unlock()
	if left := s.opts.MaxRuns - s.runs; len(todo) > left {
		s.exhaust = true
		if left < 0 {
			left = 0
		}
		todo = todo[:left]
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, s.opts.Workers)
	for _, i := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if s.ctx.Err() != nil {
				return
			}
			res, err := s.opts.Run(cands[i])
			v := err == nil && res.Check.Verdict == checker.Violation
			s.mu.Lock()
			s.cache[s.key(cands[i])] = v
			s.mu.Unlock()
			results[i] = boolResult(v)
		}(i)
	}
	wg.Wait()
	s.runs += len(todo)

	for i := range results {
		if results[alias[i]] == 1 {
			return i
		}
	}
	return -1
}

func boolResult(v bool) int {
	if v {
		return 1
	}
	return -1
}

// accept makes candidate c current.
func (s *shrinker) accept(c explore.Config) {
	c.Plan.Duration = c.Duration
	s.cur = c
}

// ---------------------------------------------------------------------------
// Faults: ddmin.

func (s *shrinker) ddmin() {
	before := len(s.cur.Plan.Faults)
	// Perhaps no fault is needed at all.
	if i := s.first([]explore.Config{s.with(nil)}); i == 0 {
		s.accept(s.with(nil))
		s.step("faults", fmt.Sprintf("ddmin: the bug needs no faults at all (%d → 0)", before))
		return
	}
	c := s.cur.Plan.Faults
	n := 2
	for len(c) >= 2 && !s.exhaust && s.ctx.Err() == nil {
		chunks := split(c, n)
		cands := make([]explore.Config, len(chunks))
		for i, ch := range chunks {
			cands[i] = s.with(ch)
		}
		if i := s.first(cands); i >= 0 {
			c = chunks[i]
			s.accept(cands[i])
			n = 2
			continue
		}
		if n > 2 || len(chunks) > 2 {
			comps := make([]explore.Config, len(chunks))
			compFaults := make([][]nemesis.Fault, len(chunks))
			for i := range chunks {
				compFaults[i] = complement(c, chunks[i])
				comps[i] = s.with(compFaults[i])
			}
			if i := s.first(comps); i >= 0 {
				c = compFaults[i]
				s.accept(comps[i])
				n = max(n-1, 2)
				continue
			}
		}
		if n >= len(c) {
			break
		}
		n = min(2*n, len(c))
	}
	s.step("faults", fmt.Sprintf("ddmin: %d → %d faults", before, len(s.cur.Plan.Faults)))
}

func split(fs []nemesis.Fault, n int) [][]nemesis.Fault {
	var out [][]nemesis.Fault
	size := len(fs) / n
	rem := len(fs) % n
	at := 0
	for i := 0; i < n; i++ {
		k := size
		if i < rem {
			k++
		}
		if k == 0 {
			continue
		}
		out = append(out, fs[at:at+k])
		at += k
	}
	return out
}

func complement(all, part []nemesis.Fault) []nemesis.Fault {
	drop := map[int]bool{}
	for _, f := range part {
		drop[f.ID] = true
	}
	var out []nemesis.Fault
	for _, f := range all {
		if !drop[f.ID] {
			out = append(out, f)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Per-fault simplification.

// mutate returns the current faults with fault i transformed by fn.
func (s *shrinker) mutate(i int, fn func(f *nemesis.Fault)) explore.Config {
	fs := clonePlan(*s.cur.Plan).Faults
	fn(&fs[i])
	return s.with(fs)
}

func (s *shrinker) shrinkTargets() {
	removed := 0
	for i := range s.cur.Plan.Faults {
		for !s.exhaust {
			f := s.cur.Plan.Faults[i]
			if len(f.Nodes) <= 1 {
				break
			}
			var cands []explore.Config
			for j := range f.Nodes {
				j := j
				cands = append(cands, s.mutate(i, func(f *nemesis.Fault) {
					f.Nodes = append(append([]int(nil), f.Nodes[:j]...), f.Nodes[j+1:]...)
				}))
			}
			k := s.first(cands)
			if k < 0 {
				break
			}
			s.accept(cands[k])
			removed++
		}
	}
	s.step("targets", fmt.Sprintf("removed %d fault targets", removed))
}

// searchMin finds (approximately) the smallest v in (lo, hi] such that
// build(v) still violates, given that hi is known to. It probes several
// points per round in parallel and returns hi unchanged if nothing smaller
// works.
func (s *shrinker) searchMin(lo, hi, grain int64, build func(v int64) explore.Config) int64 {
	for hi-lo > grain && !s.exhaust && s.ctx.Err() == nil {
		k := int64(s.opts.Workers)
		if k < 2 {
			k = 2
		}
		if k > 8 {
			k = 8
		}
		var pts []int64
		for i := int64(1); i <= k; i++ {
			p := lo + (hi-lo)*i/(k+1)
			p = (p + grain - 1) / grain * grain
			if p <= lo || p >= hi || (len(pts) > 0 && pts[len(pts)-1] == p) {
				continue
			}
			pts = append(pts, p)
		}
		if len(pts) == 0 {
			break
		}
		cands := make([]explore.Config, len(pts))
		for i, p := range pts {
			cands[i] = build(p)
		}
		j := s.first(cands)
		switch {
		case j < 0:
			lo = pts[len(pts)-1]
		case j == 0:
			hi = pts[0]
		default:
			hi, lo = pts[j], pts[j-1]
		}
	}
	return hi
}

func (s *shrinker) shrinkWindows() {
	const grain = int64(10 * time.Millisecond)
	for i := range s.cur.Plan.Faults {
		f := s.cur.Plan.Faults[i]
		length := int64(f.End - f.Start)
		// Keep the start, pull the end in.
		best := s.searchMin(0, length, grain, func(v int64) explore.Config {
			return s.mutate(i, func(f *nemesis.Fault) { f.End = f.Start + time.Duration(v) })
		})
		if best < length {
			s.accept(s.mutate(i, func(f *nemesis.Fault) { f.End = f.Start + time.Duration(best) }))
		}
		// Keep the end, push the start out.
		f = s.cur.Plan.Faults[i]
		length = int64(f.End - f.Start)
		best = s.searchMin(0, length, grain, func(v int64) explore.Config {
			return s.mutate(i, func(f *nemesis.Fault) { f.Start = f.End - time.Duration(v) })
		})
		if best < length {
			s.accept(s.mutate(i, func(f *nemesis.Fault) { f.Start = f.End - time.Duration(best) }))
		}
	}
	var total time.Duration
	for _, f := range s.cur.Plan.Faults {
		total += f.End - f.Start
	}
	s.step("windows", fmt.Sprintf("total fault time now %v", total.Round(time.Millisecond)))
}

// roundTimes snaps every fault boundary to the coarsest grid (100ms, 10ms,
// 1ms) that still reproduces the bug, so reports read "1.2s → 1.8s" instead
// of nanoseconds. Boundaries the bug is sensitive to keep their precision,
// which is itself a finding worth reading.
func (s *shrinker) roundTimes() {
	grains := []time.Duration{100 * time.Millisecond, 10 * time.Millisecond, time.Millisecond}
	snapped, exact := 0, 0
	for i := range s.cur.Plan.Faults {
		for _, end := range []bool{false, true} {
			f := s.cur.Plan.Faults[i]
			var cands []explore.Config
			for _, g := range grains {
				g := g
				v := f.Start / g * g
				if end {
					v = (f.End + g - 1) / g * g
				}
				if v == f.Start && !end || v == f.End && end {
					break // already on this grid, hence on every finer one
				}
				cands = append(cands, s.mutate(i, func(f *nemesis.Fault) {
					if end {
						f.End = v
					} else {
						f.Start = v
					}
				}))
			}
			if len(cands) == 0 {
				continue
			}
			if k := s.first(cands); k >= 0 {
				s.accept(cands[k])
				snapped++
			} else {
				exact++
			}
		}
	}
	note := fmt.Sprintf("snapped %d fault boundaries to a coarse grid", snapped)
	if exact > 0 {
		note += fmt.Sprintf("; %d timing-critical boundaries kept exact", exact)
	}
	s.step("round", note)
}

func (s *shrinker) shrinkParams() {
	softened := 0
	try := func(i int, fn func(f *nemesis.Fault)) bool {
		c := s.mutate(i, fn)
		if s.first([]explore.Config{c}) == 0 {
			s.accept(c)
			softened++
			return true
		}
		return false
	}
	for i := range s.cur.Plan.Faults {
		f := s.cur.Plan.Faults[i]
		switch f.Kind {
		case nemesis.Latency:
			ms := int64(f.Delay / time.Millisecond)
			best := s.searchMin(0, ms, 1, func(v int64) explore.Config {
				return s.mutate(i, func(f *nemesis.Fault) { f.Delay = time.Duration(v) * time.Millisecond; f.Jitter = f.Delay / 4 })
			})
			if best < ms {
				s.accept(s.mutate(i, func(f *nemesis.Fault) { f.Delay = time.Duration(best) * time.Millisecond; f.Jitter = f.Delay / 4 }))
				softened++
			}
		case nemesis.Loss:
			pct := int64(f.Loss * 100)
			best := s.searchMin(0, pct, 1, func(v int64) explore.Config {
				return s.mutate(i, func(f *nemesis.Fault) { f.Loss = float64(v) / 100 })
			})
			if best < pct {
				s.accept(s.mutate(i, func(f *nemesis.Fault) { f.Loss = float64(best) / 100 }))
				softened++
			}
		case nemesis.ClockSkew:
			if f.Rate != 1 {
				try(i, func(f *nemesis.Fault) { f.Rate = 1 })
			}
			if f.Offset != 0 {
				if !try(i, func(f *nemesis.Fault) { f.Offset = 0 }) {
					sign := time.Duration(1)
					mag := s.cur.Plan.Faults[i].Offset
					if mag < 0 {
						sign, mag = -1, -mag
					}
					const grain = int64(100 * time.Millisecond)
					best := s.searchMin(0, int64(mag), grain, func(v int64) explore.Config {
						return s.mutate(i, func(f *nemesis.Fault) { f.Offset = sign * time.Duration(v) })
					})
					if best < int64(mag) {
						s.accept(s.mutate(i, func(f *nemesis.Fault) { f.Offset = sign * time.Duration(best) }))
						softened++
					}
				}
			}
		}
	}
	s.step("params", fmt.Sprintf("softened %d fault parameters", softened))
}

// ---------------------------------------------------------------------------
// Workload.

func (s *shrinker) shrinkDuration(phase string) {
	before := s.cur.Duration
	const grain = int64(10 * time.Millisecond)
	best := s.searchMin(0, int64(s.cur.Duration), grain, func(v int64) explore.Config {
		c := s.with(s.cur.Plan.Faults)
		c.Duration = time.Duration(v)
		return c
	})
	if time.Duration(best) < s.cur.Duration {
		c := s.with(s.cur.Plan.Faults)
		c.Duration = time.Duration(best)
		s.accept(c)
	}
	// Faults that start after the last client operation can only be noise.
	var keep []nemesis.Fault
	for _, f := range s.cur.Plan.Faults {
		if f.Start < s.cur.Duration+s.cur.OpTimeout {
			keep = append(keep, f)
		}
	}
	if len(keep) < len(s.cur.Plan.Faults) {
		if c := s.with(keep); s.first([]explore.Config{c}) == 0 {
			s.accept(c)
		}
	}
	s.step(phase, fmt.Sprintf("%v → %v", before.Round(time.Millisecond), s.cur.Duration.Round(time.Millisecond)))
}

func (s *shrinker) shrinkClients() {
	before := s.cur.Clients
	var cands []explore.Config
	for n := 1; n < s.cur.Clients; n++ {
		c := s.with(s.cur.Plan.Faults)
		c.Clients = n
		cands = append(cands, c)
	}
	if i := s.first(cands); i >= 0 {
		s.accept(cands[i])
	}
	s.step("clients", fmt.Sprintf("%d → %d", before, s.cur.Clients))
}

// ---------------------------------------------------------------------------
// Verification: 1-minimality over faults.

func (s *shrinker) verify() {
	dropped := 0
	for !s.exhaust && len(s.cur.Plan.Faults) > 0 {
		fs := s.cur.Plan.Faults
		cands := make([]explore.Config, len(fs))
		for i := range fs {
			cands[i] = s.with(complement(fs, fs[i:i+1]))
		}
		i := s.first(cands)
		if i < 0 {
			break
		}
		s.accept(cands[i])
		dropped++
	}
	ids := make([]int, len(s.cur.Plan.Faults))
	for i, f := range s.cur.Plan.Faults {
		ids[i] = f.ID
	}
	sort.Ints(ids)
	note := fmt.Sprintf("1-minimal: every remaining fault %v is necessary", ids)
	if dropped > 0 {
		note = fmt.Sprintf("dropped %d now-redundant faults; ", dropped) + note
	}
	if s.exhaust {
		note = "run budget exhausted; result may not be 1-minimal"
	}
	s.step("verify", note)
}
