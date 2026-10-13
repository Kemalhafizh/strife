// Package model defines sequential specifications that a concurrent history
// is checked against.
//
// A Model is a deterministic state machine. Step answers: "if this operation
// took effect atomically in state s, could it have produced this output, and
// what is the next state?" The linearizability checker searches for an order
// of operations, consistent with real time, in which every Step succeeds.
package model

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/kemalhafizh/strife/pkg/history"
)

// State is an immutable model state.
type State interface {
	Hash() uint64
	Equal(State) bool
	String() string
}

// Model is a sequential specification.
type Model interface {
	Name() string
	Init() State
	// Step applies in with observed completion out. out.Type is history.OK
	// or history.Info (outcome unknown: accept whatever the op would do).
	Step(s State, in, out history.Op) (bool, State)
	// Relevant reports whether an operation constrains the search at all.
	// Failed operations never happened and unknown reads observe nothing.
	Relevant(op history.Operation) bool
	// Partition splits a history into independently checkable sub-histories
	// (P-compositionality). Returning a single partition is always valid.
	Partition(ops []history.Operation) (keys []string, parts [][]history.Operation)
}

// Diagnoser is implemented by models that can search a full partition for a
// provable anomaly: a handful of operations whose real-time order and
// observed values already rule out every linearization. A diagnosis must be
// sound: if Diagnose reports one, the partition is not linearizable.
type Diagnoser interface {
	// Diagnose returns the indices of the witnessing operations, earliest
	// first, and a sentence explaining the anomaly, or ok=false.
	Diagnose(ops []history.Operation) (witness []int, diagnosis string, ok bool)
}

// ReadOnly is implemented by models whose some operations never change the
// state (reads). The checker linearizes such operations eagerly, which prunes
// the search without losing any solution.
type ReadOnly interface {
	ReadOnly(in history.Op) bool
}

// ByName returns a built-in model.
func ByName(name string) (Model, error) {
	switch strings.ToLower(name) {
	case "kv", "register", "cas-register":
		return KV{}, nil
	case "set":
		return Set{}, nil
	}
	return nil, fmt.Errorf("model: unknown model %q (want kv or set)", name)
}

// Names lists the built-in models.
func Names() []string { return []string{"kv", "set"} }

func partitionByKey(ops []history.Operation) ([]string, [][]history.Operation) {
	idx := map[string]int{}
	var keys []string
	var parts [][]history.Operation
	for _, o := range ops {
		i, ok := idx[o.Input.Key]
		if !ok {
			i = len(keys)
			idx[o.Input.Key] = i
			keys = append(keys, o.Input.Key)
			parts = append(parts, nil)
		}
		parts[i] = append(parts[i], o)
	}
	// Report keys in a stable, human friendly order.
	order := make([]int, len(keys))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return keys[order[a]] < keys[order[b]] })
	sk := make([]string, len(keys))
	sp := make([][]history.Operation, len(keys))
	for i, j := range order {
		sk[i], sp[i] = keys[j], parts[j]
	}
	return sk, sp
}

// ---------------------------------------------------------------------------
// KV: a map of independent compare-and-set registers.

// KV models a key-value store whose keys are independent read/write/cas
// registers. Keys are checked separately, which keeps search tractable.
type KV struct{}

type register struct {
	set bool
	v   int64
}

func (r register) Hash() uint64 {
	if !r.set {
		return 0x9ae16a3b2f90404f
	}
	return uint64(r.v)*0x9e3779b97f4a7c15 + 1
}

func (r register) Equal(o State) bool { return r == o.(register) }

func (r register) String() string {
	if !r.set {
		return "nil"
	}
	return fmt.Sprint(r.v)
}

func (r register) is(v *int64) bool {
	if v == nil {
		return !r.set
	}
	return r.set && r.v == *v
}

// Name implements Model.
func (KV) Name() string { return "kv" }

// ReadOnly implements ReadOnly.
func (KV) ReadOnly(in history.Op) bool { return in.F == history.FRead }

// Init implements Model.
func (KV) Init() State { return register{} }

// Relevant implements Model.
func (KV) Relevant(o history.Operation) bool {
	switch o.Status() {
	case history.Fail:
		return false
	case history.Info:
		return o.Input.F != history.FRead
	}
	return true
}

// Step implements Model.
func (KV) Step(s State, in, out history.Op) (bool, State) {
	r := s.(register)
	switch in.F {
	case history.FWrite:
		if in.Value == nil {
			return true, register{}
		}
		return true, register{set: true, v: *in.Value}
	case history.FRead:
		if out.Type != history.OK {
			return true, r
		}
		return r.is(out.Value), r
	case history.FCas:
		if r.is(in.Old) {
			if in.Value == nil {
				return true, register{}
			}
			return true, register{set: true, v: *in.Value}
		}
		// A cas that is known to have succeeded cannot happen here; one with
		// an unknown outcome simply has no effect.
		return out.Type != history.OK, r
	}
	return false, r
}

// Partition implements Model.
func (KV) Partition(ops []history.Operation) ([]string, [][]history.Operation) {
	return partitionByKey(ops)
}

// written returns the value an operation may have stored, if it writes.
func written(o history.Operation) (*int64, bool) {
	switch o.Input.F {
	case history.FWrite:
		return o.Input.Value, true
	case history.FCas:
		if o.Status() == history.OK || o.Status() == history.Info {
			return o.Input.Value, true
		}
	}
	return nil, false
}

func sameVal(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// observed returns the value an operation proves the register held: a
// successful read's result or a successful cas's expected value.
func observed(o history.Operation) (*int64, bool) {
	if o.Status() != history.OK {
		return nil, false
	}
	switch o.Input.F {
	case history.FRead:
		return o.Output.Value, true
	case history.FCas:
		return o.Input.Old, true
	}
	return nil, false
}

// Diagnose implements Diagnoser. It looks for the earliest operation r that
// observed a value v although
//
//   - some operation o that definitely wrote another value had completed
//     before r began (so o is linearized before r), and
//   - no operation that may have written v can be linearized after o and
//     before r (each such write either finished before o began or started
//     after r returned).
//
// Then the register cannot hold v when r takes effect: no linearization
// exists. A value that nothing could have written is the degenerate case.
// It runs in O(n log n), so it can run before every search.
func (KV) Diagnose(ops []history.Operation) ([]int, string, bool) {
	type val struct {
		set bool
		v   int64
	}
	key := func(p *int64) val {
		if p == nil {
			return val{}
		}
		return val{true, *p}
	}

	// Writers of each value, sorted by invocation, with prefix maxima of
	// their completion times (Infinity for indeterminate writes).
	type byVal struct {
		idx   []int   // op indices sorted by Call
		calls []int64 // their Call times
		// top1/top2: the two ops (indices into ops, -1 if absent) with the
		// latest Return among idx[:j+1].
		top1, top2 []int
	}
	writers := map[val]*byVal{}
	// Definite writers (status ok) sorted by completion, with the latest and
	// the latest of a different value among each prefix.
	var definite []int
	for i, o := range ops {
		w, ok := written(o)
		if !ok {
			continue
		}
		k := key(w)
		bv := writers[k]
		if bv == nil {
			bv = &byVal{}
			writers[k] = bv
		}
		bv.idx = append(bv.idx, i)
		if o.Status() == history.OK {
			definite = append(definite, i)
		}
	}
	for _, bv := range writers {
		sort.SliceStable(bv.idx, func(a, b int) bool { return ops[bv.idx[a]].Call < ops[bv.idx[b]].Call })
		bv.calls = make([]int64, len(bv.idx))
		bv.top1 = make([]int, len(bv.idx))
		bv.top2 = make([]int, len(bv.idx))
		t1, t2 := -1, -1
		for j, i := range bv.idx {
			bv.calls[j] = ops[i].Call
			switch {
			case t1 < 0 || ops[i].Return > ops[t1].Return:
				t1, t2 = i, t1
			case t2 < 0 || ops[i].Return > ops[t2].Return:
				t2 = i
			}
			bv.top1[j], bv.top2[j] = t1, t2
		}
	}
	sort.SliceStable(definite, func(a, b int) bool { return ops[definite[a]].Return < ops[definite[b]].Return })
	defRets := make([]int64, len(definite))
	best := make([]int, len(definite))   // latest writer in prefix
	second := make([]int, len(definite)) // latest writer of a different value than best, or -1
	for j, i := range definite {
		defRets[j] = ops[i].Return
		best[j], second[j] = i, -1
		if j == 0 {
			continue
		}
		pb, ps := best[j-1], second[j-1]
		wi, _ := written(ops[i])
		wpb, _ := written(ops[pb])
		if sameVal(wi, wpb) {
			second[j] = ps
		} else {
			second[j] = pb
		}
	}
	// lastBefore returns the latest definite writer that completed strictly
	// before t and wrote something other than v, or -1.
	lastBefore := func(t int64, v *int64) int {
		j := sort.Search(len(defRets), func(x int) bool { return defRets[x] >= t }) - 1
		if j < 0 {
			return -1
		}
		if w, _ := written(ops[best[j]]); !sameVal(w, v) {
			return best[j]
		}
		return second[j]
	}
	// latestProducer returns the writer of v invoked no later than t with
	// the latest completion, or -1.
	latestProducer := func(v *int64, t int64, skip int) (int, int64) {
		bv := writers[key(v)]
		if bv == nil {
			return -1, 0
		}
		j := sort.Search(len(bv.calls), func(x int) bool { return bv.calls[x] > t }) - 1
		if j < 0 {
			return -1, 0
		}
		// skip is the observer itself when it is a cas writing the value
		// it expected; the runner-up is then the latest other writer.
		i := bv.top1[j]
		if i == skip {
			i = bv.top2[j]
		}
		if i < 0 {
			return -1, 0
		}
		return i, ops[i].Return
	}

	order := make([]int, len(ops))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return ops[order[a]].Call < ops[order[b]].Call })
	for _, ri := range order {
		r := ops[ri]
		v, ok := observed(r)
		if !ok {
			continue
		}
		verb := "returned"
		if r.Input.F == history.FCas {
			verb = "succeeded comparing against"
		}
		prod, prodRet := latestProducer(v, r.Return, ri)
		if v != nil && prod < 0 {
			return []int{ri}, fmt.Sprintf("Phantom value: %s %s %s, a value no operation could have written before it.",
				r.Describe(), verb, fmtVal(v)), true
		}
		over := lastBefore(r.Call, v)
		if over < 0 {
			continue
		}
		o := ops[over]
		// Could some writer of v take effect between o and r? Its call must
		// be no later than r's return and its return no earlier than o's call.
		if prod >= 0 && prodRet >= o.Call {
			continue
		}
		kind := "Stale read"
		if r.Input.F == history.FCas {
			kind = "Stale compare-and-set"
		}
		if prod < 0 {
			return []int{over, ri}, fmt.Sprintf("%s: %s began at %s, after %s had completed at %s, yet it %s nil: the key had already been written.",
				kind, r.Describe(), fmtNs(r.Call), o.Describe(), fmtNs(o.Return), verb), true
		}
		p := ops[prod]
		return []int{prod, over, ri}, fmt.Sprintf("%s: %s began at %s, after %s had completed at %s, yet it %s %s, last written by %s, which finished at %s, before that overwrite began.",
			kind, r.Describe(), fmtNs(r.Call), o.Describe(), fmtNs(o.Return), verb, fmtVal(v), p.Describe(), fmtNs(p.Return)), true
	}
	return nil, "", false
}

func fmtVal(v *int64) string {
	if v == nil {
		return "nil"
	}
	return fmt.Sprint(*v)
}

func fmtNs(ns int64) string {
	if ns == history.Infinity {
		return "∞"
	}
	return fmt.Sprintf("%.3fs", float64(ns)/1e9)
}

// ---------------------------------------------------------------------------
// Set: a grow-only set with add and read-all.

// Set models a grow-only set: add(x) and read() returning every element.
type Set struct{}

type setState struct{ elems []int64 } // sorted, never mutated

func (s setState) Hash() uint64 {
	h := fnv.New64a()
	var b [8]byte
	for _, e := range s.elems {
		for i := 0; i < 8; i++ {
			b[i] = byte(uint64(e) >> (8 * i))
		}
		_, _ = h.Write(b[:])
	}
	return h.Sum64()
}

func (s setState) Equal(o State) bool {
	t := o.(setState)
	if len(s.elems) != len(t.elems) {
		return false
	}
	for i := range s.elems {
		if s.elems[i] != t.elems[i] {
			return false
		}
	}
	return true
}

func (s setState) String() string { return fmt.Sprint(s.elems) }

func (s setState) add(v int64) setState {
	i := sort.Search(len(s.elems), func(i int) bool { return s.elems[i] >= v })
	if i < len(s.elems) && s.elems[i] == v {
		return s
	}
	n := make([]int64, 0, len(s.elems)+1)
	n = append(n, s.elems[:i]...)
	n = append(n, v)
	n = append(n, s.elems[i:]...)
	return setState{n}
}

// Name implements Model.
func (Set) Name() string { return "set" }

// ReadOnly implements ReadOnly.
func (Set) ReadOnly(in history.Op) bool { return in.F == history.FRead }

// Init implements Model.
func (Set) Init() State { return setState{} }

// Relevant implements Model.
func (Set) Relevant(o history.Operation) bool { return KV{}.Relevant(o) }

// Step implements Model.
func (Set) Step(s State, in, out history.Op) (bool, State) {
	st := s.(setState)
	switch in.F {
	case history.FAdd:
		if in.Value == nil {
			return false, st
		}
		return true, st.add(*in.Value)
	case history.FRead:
		if out.Type != history.OK {
			return true, st
		}
		got := append([]int64(nil), out.Values...)
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		return st.Equal(setState{got}), st
	}
	return false, st
}

// Partition implements Model.
func (Set) Partition(ops []history.Operation) ([]string, [][]history.Operation) {
	return partitionByKey(ops)
}
