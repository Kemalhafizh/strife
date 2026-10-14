// Package checker decides whether a concurrent history is linearizable with
// respect to a sequential model.
//
// The search is the Wing & Gong algorithm with Lowe's memoization ("WGL"):
// call/return events are kept in a doubly linked list ordered by time; the
// search repeatedly picks a pending call, applies it to the model and lifts
// it out of the list, backtracking when it reaches a return event whose
// operation has not been linearized yet. (linearized-set, state) pairs that
// have already been explored are cached, which turns the exponential search
// into something practical for real histories. Independent keys are checked
// separately and in parallel (Herlihy & Wing's locality / P-compositionality).
package checker

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
)

// Verdict is the outcome of a check.
type Verdict string

// Verdicts.
const (
	Linearizable Verdict = "linearizable"
	Violation    Verdict = "violation"
	Unknown      Verdict = "unknown" // search budget exhausted
)

// Options tune the search.
type Options struct {
	// MaxSteps bounds the search per partition; 0 means 50 million.
	MaxSteps uint64
	// Parallelism bounds concurrent partitions; 0 means GOMAXPROCS.
	Parallelism int
	// Timeout bounds the wall-clock time spent on each partition; 0 means
	// no limit. Unlike MaxSteps it depends on the machine, so a simulated
	// run that hits it is no longer reproducible: prefer MaxSteps there.
	Timeout time.Duration
	// SearchOnly disables the diagnosis fast path, so every verdict comes
	// from the exhaustive search (benchmarks and cross-checks).
	SearchOnly bool
}

// Counterexample explains why a partition is not linearizable.
type Counterexample struct {
	// Linearized is the longest prefix of a linearization the search found,
	// as operation descriptions in linearization order.
	Linearized []Step `json:"linearized"`
	// StateAfter is the model state after Linearized.
	StateAfter string `json:"state_after"`
	// Stuck is the operation that could not be linearized before it returned.
	Stuck Step `json:"stuck"`
	// Concurrent lists the operations that were still candidates when the
	// search got stuck (none of them could be placed either).
	Concurrent []Step `json:"concurrent,omitempty"`
	// Explanation is a human readable summary of the search's dead end.
	Explanation string `json:"explanation"`
	// Witness is a minimal set of operations from the history that on its
	// own cannot be linearized (see AddWitnesses). Usually two to four
	// operations, and the best place to start reading.
	Witness []Step `json:"witness,omitempty"`
	// Diagnosis explains the witness in the model's terms, e.g. a stale read.
	Diagnosis string `json:"diagnosis,omitempty"`
}

// Summary is the most useful one-line description: the diagnosis of the
// minimal witness when available, the search's explanation otherwise.
func (c *Counterexample) Summary() string {
	if c.Diagnosis != "" {
		return c.Diagnosis
	}
	return c.Explanation
}

// Step identifies one operation in a counterexample.
type Step struct {
	Index       int    `json:"index"` // index of the invoke event in the history
	Process     int    `json:"process"`
	Call        int64  `json:"call"`
	Return      int64  `json:"return"` // history.Infinity if unknown
	Description string `json:"description"`
}

// KeyResult is the verdict for one partition.
type KeyResult struct {
	Key     string  `json:"key"`
	Verdict Verdict `json:"verdict"`
	// Reason says why the verdict is unknown ("step budget" or "time
	// budget"); empty otherwise.
	Reason         string          `json:"reason,omitempty"`
	Operations     int             `json:"operations"`
	Steps          uint64          `json:"steps"`
	Counterexample *Counterexample `json:"counterexample,omitempty"`
}

// Report is the result of checking a whole history.
type Report struct {
	Model      string        `json:"model"`
	Verdict    Verdict       `json:"verdict"`
	Operations int           `json:"operations"`
	Checked    int           `json:"checked"` // operations relevant to the model
	Keys       []KeyResult   `json:"keys"`
	Stats      history.Stats `json:"stats"`
}

// Failures returns the partitions that are not linearizable.
func (r Report) Failures() []KeyResult {
	var out []KeyResult
	for _, k := range r.Keys {
		if k.Verdict == Violation {
			out = append(out, k)
		}
	}
	return out
}

// Check pairs a raw history and checks it.
func Check(m model.Model, ops []history.Op, opts Options) (Report, error) {
	paired, err := history.Pair(ops)
	if err != nil {
		return Report{}, err
	}
	return CheckOperations(m, paired, opts), nil
}

// CheckOperations checks already paired operations.
func CheckOperations(m model.Model, ops []history.Operation, opts Options) Report {
	if opts.MaxSteps == 0 {
		opts.MaxSteps = 50_000_000
	}
	if opts.Parallelism <= 0 {
		opts.Parallelism = runtime.GOMAXPROCS(0)
	}
	rep := Report{Model: m.Name(), Operations: len(ops), Stats: history.Summarize(ops)}
	keys, parts := partitions(m, ops)
	for _, p := range parts {
		rep.Checked += len(p)
	}
	rep.Keys = make([]KeyResult, len(parts))

	sem := make(chan struct{}, opts.Parallelism)
	var wg sync.WaitGroup
	for i := range parts {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			r := checkKey(m, parts[i], opts)
			r.Key = keys[i]
			rep.Keys[i] = r
		}(i)
	}
	wg.Wait()

	rep.Verdict = Linearizable
	for _, k := range rep.Keys {
		switch k.Verdict {
		case Violation:
			rep.Verdict = Violation
		case Unknown:
			if rep.Verdict != Violation {
				rep.Verdict = Unknown
			}
		}
	}
	return rep
}

type entry struct {
	id    int
	call  bool
	time  int64
	match *entry // call -> its return; return -> its call
	prev  *entry
	next  *entry
}

type frame struct {
	e     *entry
	state model.State
	// forced frames linearize a read-only operation eagerly; there is no
	// alternative worth trying at that level (see search).
	forced bool
}

type cacheEntry struct {
	lo    int      // first word of lin that is not all ones
	bits  []uint64 // lin's words from lo up to its last non-zero word
	state model.State
}

// checkKey checks one partition. A model that can diagnose a provable anomaly
// short-circuits the search: proving non-linearizability exhaustively is the
// expensive direction (every order must be refuted), while a diagnosis is a
// direct certificate found in O(n log n).
func checkKey(m model.Model, ops []history.Operation, opts Options) KeyResult {
	if d, ok := m.(model.Diagnoser); ok && !opts.SearchOnly {
		if idx, diag, ok := d.Diagnose(ops); ok {
			ce := &Counterexample{
				Stuck:       step(ops[idx[len(idx)-1]]),
				Diagnosis:   diag,
				Explanation: "Proved directly from the witness below; no search was needed.",
				StateAfter:  "?",
			}
			for _, i := range idx {
				ce.Witness = append(ce.Witness, step(ops[i]))
			}
			return KeyResult{Operations: len(ops), Verdict: Violation, Counterexample: ce}
		}
	}
	return checkPartition(m, ops, opts.MaxSteps, opts.Timeout)
}

// Reasons for an unknown verdict.
const (
	ReasonSteps = "step budget"
	ReasonTime  = "time budget"
)

// checkPartition decides one partition. The search runs without any
// bookkeeping first; only when it proves a violation does a second, recording
// pass reconstruct the counterexample. Recording costs O(depth) per new
// deepest dead end, which made large linearizable histories quadratic for
// no benefit.
func checkPartition(m model.Model, ops []history.Operation, maxSteps uint64, timeout time.Duration) KeyResult {
	res, _ := search(m, ops, maxSteps, timeout, false)
	if res.Verdict == Violation {
		_, best := search(m, ops, maxSteps, timeout, true)
		res.Counterexample = best.counterexample(ops)
	}
	return res
}

// deadEnd is the deepest point the search reached before backtracking: the
// operations linearized so far, the model state, the operation whose return
// could not be met, and the calls that were still candidates.
type deadEnd struct {
	linearized []int
	state      model.State
	stuck      int
	candidates []int
}

func (d *deadEnd) counterexample(ops []history.Operation) *Counterexample {
	if d == nil {
		return nil
	}
	ce := &Counterexample{StateAfter: d.state.String(), Stuck: step(ops[d.stuck])}
	for _, i := range d.linearized {
		ce.Linearized = append(ce.Linearized, step(ops[i]))
	}
	for _, i := range d.candidates {
		ce.Concurrent = append(ce.Concurrent, step(ops[i]))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "No linearization places %q before it returned. ", ce.Stuck.Description)
	fmt.Fprintf(&b, "After the longest valid prefix (%d ops) the model state is %s", len(ce.Linearized), ce.StateAfter)
	if len(ce.Concurrent) > 0 {
		fmt.Fprintf(&b, ", and none of the %d concurrent candidates can be applied first to reach a state that explains it.", len(ce.Concurrent))
	} else {
		b.WriteString(", which is inconsistent with the observed result.")
	}
	ce.Explanation = b.String()
	return ce
}

func search(m model.Model, ops []history.Operation, maxSteps uint64, timeout time.Duration, record bool) (KeyResult, *deadEnd) {
	res := KeyResult{Operations: len(ops), Verdict: Linearizable}
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	n := len(ops)
	if n == 0 {
		return res, nil
	}

	events := make([]*entry, 0, 2*n)
	for i, o := range ops {
		c := &entry{id: i, call: true, time: o.Call}
		r := &entry{id: i, call: false, time: o.Return}
		c.match, r.match = r, c
		events = append(events, c, r)
	}
	// Calls sort before returns at equal timestamps: treating touching
	// operations as concurrent can only hide anomalies, never invent them.
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.time != b.time {
			return a.time < b.time
		}
		if a.call != b.call {
			return a.call
		}
		return a.id < b.id
	})
	head := &entry{id: -1}
	prev := head
	for _, e := range events {
		e.prev = prev
		prev.next = e
		prev = e
	}

	lift := func(e *entry) {
		e.prev.next = e.next
		if e.next != nil {
			e.next.prev = e.prev
		}
		r := e.match
		r.prev.next = r.next
		if r.next != nil {
			r.next.prev = r.prev
		}
	}
	unlift := func(e *entry) {
		r := e.match
		r.prev.next = r
		if r.next != nil {
			r.next.prev = r
		}
		e.prev.next = e
		if e.next != nil {
			e.next.prev = e
		}
	}

	// Symmetry reduction: indeterminate operations never return, so two of
	// them with identical inputs are interchangeable once both have been
	// invoked. Only the earliest unlinearized twin is ever tried, which
	// collapses k! equivalent orderings into one.
	twin := make([]int, n)
	last := map[string]int{}
	for i := range twin {
		twin[i] = -1
	}
	for _, e := range events {
		if !e.call || ops[e.id].Return != history.Infinity {
			continue
		}
		in := ops[e.id].Input
		k := fmt.Sprintf("%s|%v|%v", in.F, ptr(in.Value), ptr(in.Old))
		if j, ok := last[k]; ok {
			twin[e.id] = j
		}
		last[k] = e.id
	}

	state := m.Init()
	lin := newLinSet(n)
	cache := map[uint64][]cacheEntry{}
	var stack []frame
	bestDepth := -1
	var best *deadEnd

	// Eager reads. A read-only operation never changes the state, so if a
	// candidate read matches the current state, linearizing it right now is
	// at least as good as any later position: a solution that places it
	// later can move it here (everything that must precede it is already
	// linearized, and the moves keep every real-time constraint). So at the
	// start of each level such a read is taken first, and when that subtree
	// fails the whole level fails: its alternatives need not be explored.
	ro, _ := m.(model.ReadOnly)
	cached := func(next model.State) bool {
		h := lin.hash() ^ (next.Hash() * 0x9e3779b97f4a7c15)
		for _, c := range cache[h] {
			if lin.equal(c.lo, c.bits) && c.state.Equal(next) {
				return true
			}
		}
		lo, bits := lin.snapshot()
		cache[h] = append(cache[h], cacheEntry{lo: lo, bits: bits, state: next})
		return false
	}
	eagerRead := func() *entry {
		if ro == nil {
			return nil
		}
		for c := head.next; c != nil && c.call; c = c.next {
			if ro.ReadOnly(ops[c.id].Input) {
				if ok, _ := m.Step(state, ops[c.id].Input, ops[c.id].Output); ok {
					return c
				}
			}
		}
		return nil
	}

	e := head.next
	for head.next != nil {
		res.Steps++
		if res.Steps > maxSteps {
			res.Verdict, res.Reason = Unknown, ReasonSteps
			return res, nil
		}
		if timeout > 0 && res.Steps&0xfff == 0 && time.Now().After(deadline) {
			res.Verdict, res.Reason = Unknown, ReasonTime
			return res, nil
		}
		if e.call && e == head.next {
			if c := eagerRead(); c != nil {
				lin.set(c.id)
				if !cached(state) {
					stack = append(stack, frame{e: c, state: state, forced: true})
					lift(c)
					e = head.next
					continue
				}
				// Known dead end: the level fails without alternatives.
				lin.clear(c.id)
				e = &entry{id: c.id, call: false} // fall through to backtracking
			}
		}
		if e.call {
			if t := twin[e.id]; t >= 0 && !lin.has(t) {
				e = e.next
				continue
			}
			ok, next := m.Step(state, ops[e.id].Input, ops[e.id].Output)
			if ok {
				lin.set(e.id)
				if !cached(next) {
					stack = append(stack, frame{e: e, state: state})
					state = next
					lift(e)
					e = head.next
					continue
				}
				lin.clear(e.id)
			}
			e = e.next
			continue
		}

		// A return event: its operation must already have been linearized,
		// so the current prefix is a dead end.
		if record && len(stack) > bestDepth && e.match != nil {
			bestDepth = len(stack)
			best = &deadEnd{state: state, stuck: e.id}
			for _, f := range stack {
				best.linearized = append(best.linearized, f.e.id)
			}
			for c := head.next; c != nil && c != e; c = c.next {
				if c.call && c.id != e.id {
					best.candidates = append(best.candidates, c.id)
				}
			}
		}
		for {
			if len(stack) == 0 {
				res.Verdict = Violation
				return res, best
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			state = top.state
			lin.clear(top.e.id)
			unlift(top.e)
			e = top.e.next
			if !top.forced {
				break
			}
		}
	}
	return res, nil
}

func step(o history.Operation) Step {
	return Step{Index: o.Input.Index, Process: o.Process, Call: o.Call, Return: o.Return, Description: o.Describe()}
}

func ptr(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// linSet is the set of linearized operation ids. Operations are linearized
// roughly in invocation order, so the set is almost always "every id below
// some watermark, plus a few above it". The words below lo are all ones and
// the words from hi on are all zero, so cache entries only store the window
// [lo, hi): memory per entry stays proportional to the concurrency, not to
// the length of the history (a full copy made 100k-op histories quadratic).
type linSet struct {
	w      []uint64
	lo, hi int
}

func newLinSet(n int) *linSet { return &linSet{w: make([]uint64, (n+63)/64)} }

func (l *linSet) has(i int) bool { return l.w[i/64]&(1<<(uint(i)%64)) != 0 }

func (l *linSet) set(i int) {
	k := i / 64
	l.w[k] |= 1 << (uint(i) % 64)
	if k >= l.hi {
		l.hi = k + 1
	}
	for l.lo < len(l.w) && l.w[l.lo] == ^uint64(0) {
		l.lo++
	}
}

func (l *linSet) clear(i int) {
	k := i / 64
	l.w[k] &^= 1 << (uint(i) % 64)
	if k < l.lo {
		l.lo = k
	}
	for l.hi > 0 && l.w[l.hi-1] == 0 {
		l.hi--
	}
}

func (l *linSet) window() []uint64 {
	if l.hi <= l.lo {
		return nil
	}
	return l.w[l.lo:l.hi]
}

func (l *linSet) hash() uint64 {
	h := uint64(14695981039346656037) ^ uint64(l.lo)*0x9e3779b97f4a7c15
	for _, x := range l.window() {
		h ^= x
		h *= 1099511628211
		h ^= h >> 29
	}
	return h
}

func (l *linSet) snapshot() (int, []uint64) {
	return l.lo, append([]uint64(nil), l.window()...)
}

func (l *linSet) equal(lo int, bits []uint64) bool {
	win := l.window()
	if lo != l.lo || len(bits) != len(win) {
		return false
	}
	for i := range win {
		if win[i] != bits[i] {
			return false
		}
	}
	return true
}
