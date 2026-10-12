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
	// Explanation is a human readable summary.
	Explanation string `json:"explanation"`
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
	Key            string          `json:"key"`
	Verdict        Verdict         `json:"verdict"`
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
	var relevant []history.Operation
	for _, o := range ops {
		if m.Relevant(o) {
			relevant = append(relevant, o)
		}
	}
	rep.Checked = len(relevant)
	keys, parts := m.Partition(relevant)
	rep.Keys = make([]KeyResult, len(parts))

	sem := make(chan struct{}, opts.Parallelism)
	var wg sync.WaitGroup
	for i := range parts {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			r := checkPartition(m, parts[i], opts.MaxSteps)
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
}

type cacheEntry struct {
	bits  bitset
	state model.State
}

func checkPartition(m model.Model, ops []history.Operation, maxSteps uint64) KeyResult {
	res := KeyResult{Operations: len(ops), Verdict: Linearizable}
	n := len(ops)
	if n == 0 {
		return res
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
	lin := newBitset(n)
	cache := map[uint64][]cacheEntry{}
	var stack []frame
	bestDepth := -1
	var best *Counterexample

	e := head.next
	for head.next != nil {
		res.Steps++
		if res.Steps > maxSteps {
			res.Verdict = Unknown
			return res
		}
		if e.call {
			if t := twin[e.id]; t >= 0 && !lin.has(t) {
				e = e.next
				continue
			}
			ok, next := m.Step(state, ops[e.id].Input, ops[e.id].Output)
			if ok {
				lin.set(e.id)
				h := lin.hash() ^ (next.Hash() * 0x9e3779b97f4a7c15)
				seen := false
				for _, c := range cache[h] {
					if c.bits.equal(lin) && c.state.Equal(next) {
						seen = true
						break
					}
				}
				if !seen {
					cache[h] = append(cache[h], cacheEntry{bits: lin.clone(), state: next})
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
		if len(stack) > bestDepth {
			bestDepth = len(stack)
			best = explain(ops, stack, state, e, head)
		}
		if len(stack) == 0 {
			res.Verdict = Violation
			res.Counterexample = best
			return res
		}
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		state = top.state
		lin.clear(top.e.id)
		unlift(top.e)
		e = top.e.next
	}
	return res
}

func step(o history.Operation) Step {
	return Step{Index: o.Input.Index, Process: o.Process, Call: o.Call, Return: o.Return, Description: o.Describe()}
}

func explain(ops []history.Operation, stack []frame, state model.State, stuck *entry, head *entry) *Counterexample {
	ce := &Counterexample{StateAfter: state.String(), Stuck: step(ops[stuck.id])}
	for _, f := range stack {
		ce.Linearized = append(ce.Linearized, step(ops[f.e.id]))
	}
	// Candidates: calls still in the list before the stuck return.
	for e := head.next; e != nil && e != stuck; e = e.next {
		if e.call && e.id != stuck.id {
			ce.Concurrent = append(ce.Concurrent, step(ops[e.id]))
		}
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

// bitset is a fixed size set of operation ids.
type bitset []uint64

func newBitset(n int) bitset { return make(bitset, (n+63)/64) }

func ptr(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func (b bitset) has(i int) bool { return b[i/64]&(1<<(uint(i)%64)) != 0 }
func (b bitset) set(i int)      { b[i/64] |= 1 << (uint(i) % 64) }
func (b bitset) clear(i int)    { b[i/64] &^= 1 << (uint(i) % 64) }

func (b bitset) clone() bitset { return append(bitset(nil), b...) }

func (b bitset) equal(o bitset) bool {
	for i := range b {
		if b[i] != o[i] {
			return false
		}
	}
	return true
}

func (b bitset) hash() uint64 {
	h := uint64(14695981039346656037)
	for _, w := range b {
		h ^= w
		h *= 1099511628211
		h ^= h >> 29
	}
	return h
}
