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
	"github.com/kemalhafizh/strife/pkg/txn"
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
	// A part may share ops' backing array.
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

// Decider is implemented by models that are not checked by searching for a
// linearization at all but by a direct decision procedure, such as Elle's
// dependency-graph analysis of list-append transactions. Decide must be
// sound: a reported violation is a proof. A model that implements Decider is
// never searched, so a pass means "no anomaly found".
type Decider interface {
	// Decide returns whether ops violate the model, the indices of the
	// operations that witness the first violation, a sentence explaining
	// it, and the kinds of every violation found (e.g. "G2").
	Decide(ops []history.Operation) (violation bool, witness []int, diagnosis string, kinds []string)
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
	case "queue", "fifo", "fifo-queue":
		return Queue{}, nil
	case "counter":
		return Counter{}, nil
	case "idgen", "uid", "unique-id":
		return IDGen{}, nil
	case "txn", "transaction", "transactional":
		return TxnModel{}, nil
	case "append", "list-append", "elle":
		return Append{}, nil
	}
	return nil, fmt.Errorf("model: unknown model %q (want %s)", name, strings.Join(Names(), ", "))
}

// Names lists the built-in models.
func Names() []string { return []string{"kv", "set", "queue", "counter", "idgen", "txn", "append"} }

func partitionByKey(ops []history.Operation) ([]string, [][]history.Operation) {
	// Two passes so that each part is allocated once at its final size:
	// operations are large and growing the parts by appending copied most
	// of a big history several times over.
	idx := map[string]int{}
	var keys []string
	var count []int
	for i := range ops {
		k := ops[i].Input.Key
		j, ok := idx[k]
		if !ok {
			j = len(keys)
			idx[k] = j
			keys = append(keys, k)
			count = append(count, 0)
		}
		count[j]++
	}
	if len(keys) == 1 {
		return keys, [][]history.Operation{ops}
	}
	parts := make([][]history.Operation, len(keys))
	for j, c := range count {
		parts[j] = make([]history.Operation, 0, c)
	}
	for i := range ops {
		j := idx[ops[i].Input.Key]
		parts[j] = append(parts[j], ops[i])
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

// ---------------------------------------------------------------------------
// Queue: a FIFO queue with enqueue and dequeue.

// Queue models a FIFO queue: enqueue(x) and dequeue() returning the oldest element.
type Queue struct{}

type queueState struct {
	elems []int64 // oldest at 0, newest at end
}

func (q queueState) Hash() uint64 {
	h := fnv.New64a()
	var b [8]byte
	for _, e := range q.elems {
		for i := 0; i < 8; i++ {
			b[i] = byte(uint64(e) >> (8 * i))
		}
		_, _ = h.Write(b[:])
	}
	return h.Sum64()
}

func (q queueState) Equal(o State) bool {
	t, ok := o.(queueState)
	if !ok || len(q.elems) != len(t.elems) {
		return false
	}
	for i := range q.elems {
		if q.elems[i] != t.elems[i] {
			return false
		}
	}
	return true
}

func (q queueState) String() string { return fmt.Sprint(q.elems) }

func (q queueState) enqueue(v int64) queueState {
	n := make([]int64, len(q.elems)+1)
	copy(n, q.elems)
	n[len(q.elems)] = v
	return queueState{elems: n}
}

func (q queueState) dequeue() queueState {
	if len(q.elems) == 0 {
		return q
	}
	n := make([]int64, len(q.elems)-1)
	copy(n, q.elems[1:])
	return queueState{elems: n}
}

// Name implements Model.
func (Queue) Name() string { return "queue" }

// Init implements Model.
func (Queue) Init() State { return queueState{} }

// Relevant implements Model.
func (Queue) Relevant(o history.Operation) bool {
	switch o.Status() {
	case history.Fail:
		return false
	case history.Info:
		return isEnqueue(o.Input.F)
	}
	return true
}

func isEnqueue(f string) bool {
	return f == history.FEnqueue || f == "push" || f == "send" || f == history.FAdd
}

func isDequeue(f string) bool {
	return f == history.FDequeue || f == "pop" || f == "poll" || f == history.FRead
}

// Step implements Model.
func (Queue) Step(s State, in, out history.Op) (bool, State) {
	st := s.(queueState)
	switch {
	case isEnqueue(in.F):
		if in.Value == nil {
			return false, st
		}
		return true, st.enqueue(*in.Value)
	case isDequeue(in.F):
		if out.Type != history.OK {
			return true, st
		}
		if len(st.elems) == 0 {
			if out.Value == nil {
				return true, st
			}
			return false, st
		}
		head := st.elems[0]
		if out.Value == nil || *out.Value != head {
			return false, st
		}
		return true, st.dequeue()
	}
	return false, st
}

// Partition implements Model.
func (Queue) Partition(ops []history.Operation) ([]string, [][]history.Operation) {
	return partitionByKey(ops)
}

// Diagnose implements Diagnoser for Queue.
func (Queue) Diagnose(ops []history.Operation) ([]int, string, bool) {
	// 1. Phantom elements: a dequeue returned a value that was never enqueued
	// or where all enqueues of that value began strictly after the dequeue returned.
	for di, d := range ops {
		if !isDequeue(d.Input.F) || d.Status() != history.OK || d.Output.Value == nil {
			continue
		}
		val := *d.Output.Value
		foundEnqueue := false
		earliestEnqueue := -1
		for ei, e := range ops {
			if isEnqueue(e.Input.F) && e.Input.Value != nil && *e.Input.Value == val {
				if e.Call <= d.Return {
					foundEnqueue = true
					break
				}
				if earliestEnqueue < 0 || e.Call < ops[earliestEnqueue].Call {
					earliestEnqueue = ei
				}
			}
		}
		if !foundEnqueue {
			if earliestEnqueue >= 0 {
				ee := ops[earliestEnqueue]
				return []int{di, earliestEnqueue}, fmt.Sprintf(
					"Phantom dequeue: %s returned %d at %s, but that value was not enqueued until %s at %s.",
					d.Describe(), val, fmtNs(d.Return), ee.Describe(), fmtNs(ee.Call)), true
			}
			return []int{di}, fmt.Sprintf(
				"Phantom dequeue: %s returned %d at %s, but that value was never enqueued.",
				d.Describe(), val, fmtNs(d.Return)), true
		}
	}

	// 2. FIFO order inversion:
	// If enqueue(A) completed before enqueue(B) began, then A is strictly ahead of B in FIFO order.
	// If dequeue(B) completed, but no dequeue of A occurred before dequeue(B) finished, then B was dequeued before A.
	for eaIdx, ea := range ops {
		if !isEnqueue(ea.Input.F) || ea.Status() != history.OK || ea.Input.Value == nil {
			continue
		}
		valA := *ea.Input.Value

		for ebIdx, eb := range ops {
			if !isEnqueue(eb.Input.F) || eb.Input.Value == nil || ea.Return >= eb.Call {
				continue
			}
			valB := *eb.Input.Value
			if valA == valB {
				continue
			}
			// ea completed strictly before eb began.
			for dbIdx, db := range ops {
				if !isDequeue(db.Input.F) || db.Status() != history.OK || db.Output.Value == nil || *db.Output.Value != valB {
					continue
				}
				// db is a dequeue of B. Did A get dequeued before db finished?
				dequeuedABefore := false
				daAfter := -1
				for daIdx, da := range ops {
					if !isDequeue(da.Input.F) || da.Status() != history.OK || da.Output.Value == nil || *da.Output.Value != valA {
						continue
					}
					if da.Call <= db.Return {
						dequeuedABefore = true
						break
					}
					if daAfter < 0 || da.Call < ops[daAfter].Call {
						daAfter = daIdx
					}
				}
				if !dequeuedABefore {
					witness := []int{eaIdx, ebIdx, dbIdx}
					if daAfter >= 0 {
						witness = append(witness, daAfter)
						sort.Ints(witness)
						return witness, fmt.Sprintf(
							"FIFO order inversion: %s completed at %s before %s began at %s, but %s completed at %s before %s was dequeued (%s at %s).",
							ea.Describe(), fmtNs(ea.Return), eb.Describe(), fmtNs(eb.Call), db.Describe(), fmtNs(db.Return), fmtVal(&valA), ops[daAfter].Describe(), fmtNs(ops[daAfter].Call)), true
					}
					sort.Ints(witness)
					return witness, fmt.Sprintf(
						"FIFO order inversion: %s completed at %s before %s began at %s, but %s completed at %s while %s was never dequeued.",
						ea.Describe(), fmtNs(ea.Return), eb.Describe(), fmtNs(eb.Call), db.Describe(), fmtNs(db.Return), fmtVal(&valA)), true
				}
			}
		}
	}

	// 3. Premature empty dequeue:
	// enqueue(A) completed strictly before a dequeue returning empty (nil) began,
	// and no dequeue could have consumed A.
	for eaIdx, ea := range ops {
		if !isEnqueue(ea.Input.F) || ea.Status() != history.OK || ea.Input.Value == nil {
			continue
		}
		for deIdx, de := range ops {
			if !isDequeue(de.Input.F) || de.Status() != history.OK || de.Output.Value != nil || ea.Return >= de.Call {
				continue
			}
			anyDequeue := false
			for _, d := range ops {
				if isDequeue(d.Input.F) && d.Output.Value != nil && d.Call <= de.Return && d.Return >= ea.Call {
					anyDequeue = true
					break
				}
			}
			if !anyDequeue {
				return []int{eaIdx, deIdx}, fmt.Sprintf(
					"Premature empty dequeue: %s completed at %s, but %s observed an empty queue at %s with no intervening dequeue.",
					ea.Describe(), fmtNs(ea.Return), de.Describe(), fmtNs(de.Call)), true
			}
		}
	}

	return nil, "", false
}

// ---------------------------------------------------------------------------
// Counter: a shared integer counter with add and read.

// Counter models an integer counter with add(delta) and read() returning the total.
type Counter struct{}

type counterState struct {
	val int64
}

func (c counterState) Hash() uint64 {
	h := fnv.New64a()
	var b [8]byte
	for i := 0; i < 8; i++ {
		b[i] = byte(uint64(c.val) >> (8 * i))
	}
	_, _ = h.Write(b[:])
	return h.Sum64()
}

func (c counterState) Equal(o State) bool {
	t, ok := o.(counterState)
	return ok && c.val == t.val
}

func (c counterState) String() string { return fmt.Sprint(c.val) }

// Name implements Model.
func (Counter) Name() string { return "counter" }

// ReadOnly implements ReadOnly.
func (Counter) ReadOnly(in history.Op) bool {
	return in.F == history.FRead || in.F == "get"
}

// Init implements Model.
func (Counter) Init() State { return counterState{val: 0} }

// Relevant implements Model.
func (Counter) Relevant(o history.Operation) bool {
	switch o.Status() {
	case history.Fail:
		return false
	case history.Info:
		return o.Input.F != history.FRead && o.Input.F != "get"
	}
	return true
}

func isAdd(f string) bool {
	return f == history.FAdd || f == history.FInc || f == "increment"
}

func isCounterRead(f string) bool {
	return f == history.FRead || f == "get"
}

// Step implements Model.
func (Counter) Step(s State, in, out history.Op) (bool, State) {
	st := s.(counterState)
	switch {
	case isAdd(in.F):
		delta := int64(1)
		if in.Value != nil {
			delta = *in.Value
		}
		return true, counterState{val: st.val + delta}
	case isCounterRead(in.F):
		if out.Type != history.OK {
			return true, st
		}
		if out.Value == nil {
			return false, st
		}
		return *out.Value == st.val, st
	}
	return false, st
}

// Partition implements Model.
func (Counter) Partition(ops []history.Operation) ([]string, [][]history.Operation) {
	return partitionByKey(ops)
}

// Diagnose implements Diagnoser for Counter.
func (Counter) Diagnose(ops []history.Operation) ([]int, string, bool) {
	// Check if all increments are non-negative
	allNonNeg := true
	for _, o := range ops {
		if isAdd(o.Input.F) && o.Input.Value != nil && *o.Input.Value < 0 {
			allNonNeg = false
			break
		}
	}
	if !allNonNeg {
		return nil, "", false
	}

	for ri, r := range ops {
		if !isCounterRead(r.Input.F) || r.Status() != history.OK || r.Output.Value == nil {
			continue
		}
		observedVal := *r.Output.Value
		if observedVal < 0 {
			return []int{ri}, fmt.Sprintf(
				"Negative counter value: %s returned %d at %s, but the counter begins at 0 and increments are non-negative.",
				r.Describe(), observedVal, fmtNs(r.Return)), true
		}

		// Sum completed adds that returned strictly before r began
		var completedSum int64
		var completedAdds []int
		for ai, a := range ops {
			if isAdd(a.Input.F) && a.Status() == history.OK && a.Return < r.Call {
				delta := int64(1)
				if a.Input.Value != nil {
					delta = *a.Input.Value
				}
				completedSum += delta
				completedAdds = append(completedAdds, ai)
			}
		}

		if observedVal < completedSum {
			// Select minimal subset of completedAdds whose sum already exceeds observedVal
			var minAdds []int
			var running int64
			for _, ai := range completedAdds {
				delta := int64(1)
				if ops[ai].Input.Value != nil {
					delta = *ops[ai].Input.Value
				}
				running += delta
				minAdds = append(minAdds, ai)
				if running > observedVal {
					break
				}
			}
			witness := append(minAdds, ri)
			sort.Ints(witness)
			return witness, fmt.Sprintf(
				"Stale counter read: %s returned %d at %s, but completed increments totaling %d had already finished before the read began.",
				r.Describe(), observedVal, fmtNs(r.Return), running), true
		}

		// Check if observedVal exceeds all additions that were invoked before r completed
		var allInitiatedSum int64
		for _, a := range ops {
			if isAdd(a.Input.F) && a.Status() != history.Fail && a.Call <= r.Return {
				delta := int64(1)
				if a.Input.Value != nil {
					delta = *a.Input.Value
				}
				allInitiatedSum += delta
			}
		}
		if observedVal > allInitiatedSum {
			return []int{ri}, fmt.Sprintf(
				"Overstated counter read: %s returned %d at %s, but the sum of all initiated increments is only %d.",
				r.Describe(), observedVal, fmtNs(r.Return), allInitiatedSum), true
		}
	}

	return nil, "", false
}

// ---------------------------------------------------------------------------
// IDGen: a unique identifier generator.

// IDGen models a unique identifier generator where every generated ID is distinct.
type IDGen struct{}

type idGenState struct {
	seen []int64 // sorted list of generated IDs
}

func (s idGenState) Hash() uint64 {
	h := fnv.New64a()
	var b [8]byte
	for _, e := range s.seen {
		for i := 0; i < 8; i++ {
			b[i] = byte(uint64(e) >> (8 * i))
		}
		_, _ = h.Write(b[:])
	}
	return h.Sum64()
}

func (s idGenState) Equal(o State) bool {
	t, ok := o.(idGenState)
	if !ok || len(s.seen) != len(t.seen) {
		return false
	}
	for i := range s.seen {
		if s.seen[i] != t.seen[i] {
			return false
		}
	}
	return true
}

func (s idGenState) String() string { return fmt.Sprint(s.seen) }

// Name implements Model.
func (IDGen) Name() string { return "idgen" }

// Init implements Model.
func (IDGen) Init() State { return idGenState{} }

// Relevant implements Model.
func (IDGen) Relevant(o history.Operation) bool {
	return o.Status() != history.Fail
}

// Step implements Model.
func (IDGen) Step(s State, in, out history.Op) (bool, State) {
	st := s.(idGenState)
	if out.Type != history.OK {
		return true, st
	}
	if out.Value == nil {
		return false, st
	}
	id := *out.Value
	idx := sort.Search(len(st.seen), func(i int) bool { return st.seen[i] >= id })
	if idx < len(st.seen) && st.seen[idx] == id {
		// Duplicate ID: violation!
		return false, st
	}
	next := make([]int64, len(st.seen)+1)
	copy(next[:idx], st.seen[:idx])
	next[idx] = id
	copy(next[idx+1:], st.seen[idx:])
	return true, idGenState{seen: next}
}

// Partition implements Model.
func (IDGen) Partition(ops []history.Operation) ([]string, [][]history.Operation) {
	return partitionByKey(ops)
}

// Diagnose implements Diagnoser for IDGen.
func (IDGen) Diagnose(ops []history.Operation) ([]int, string, bool) {
	seen := map[int64]int{}
	for i, o := range ops {
		if o.Status() != history.OK || o.Output.Value == nil {
			continue
		}
		id := *o.Output.Value
		if prev, ok := seen[id]; ok {
			p := ops[prev]
			return []int{prev, i}, fmt.Sprintf(
				"Duplicate ID generated: %s and %s both generated ID %d.",
				p.Describe(), o.Describe(), id), true
		}
		seen[id] = i
	}
	return nil, "", false
}

// ---------------------------------------------------------------------------
// TxnModel: multi-key transactional consistency model with Elle-style cycle detection.

// TxnModel models multi-key transactions checking for isolation anomalies (G0, G1, G2).
type TxnModel struct {
	Level string // target isolation level, default "serializable"
}

type txnState struct {
	kvs map[string]int64
}

func (s txnState) Hash() uint64 {
	h := fnv.New64a()
	var keys []string
	for k := range s.kvs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b [8]byte
	for _, k := range keys {
		_, _ = h.Write([]byte(k))
		v := s.kvs[k]
		for i := 0; i < 8; i++ {
			b[i] = byte(uint64(v) >> (8 * i))
		}
		_, _ = h.Write(b[:])
	}
	return h.Sum64()
}

func (s txnState) Equal(o State) bool {
	t, ok := o.(txnState)
	if !ok || len(s.kvs) != len(t.kvs) {
		return false
	}
	for k, v := range s.kvs {
		if t.kvs[k] != v {
			return false
		}
	}
	return true
}

func (s txnState) String() string {
	return fmt.Sprint(s.kvs)
}

// Name implements Model.
func (TxnModel) Name() string { return "txn" }

// Init implements Model.
func (TxnModel) Init() State { return txnState{kvs: make(map[string]int64)} }

// Relevant implements Model.
func (TxnModel) Relevant(o history.Operation) bool {
	return o.Status() != history.Fail
}

// Step implements Model.
func (TxnModel) Step(s State, in, out history.Op) (bool, State) {
	st := s.(txnState)
	if in.F != history.FTxn {
		return true, st
	}
	next := txnState{kvs: make(map[string]int64, len(st.kvs))}
	for k, v := range st.kvs {
		next.kvs[k] = v
	}

	for _, m := range in.Txn {
		if m.F == "w" || m.F == "write" {
			if m.Value != nil {
				next.kvs[m.Key] = *m.Value
			}
		} else {
			if out.Type == history.OK && m.Value != nil {
				cur, exists := next.kvs[m.Key]
				if !exists {
					if *m.Value != 0 && *m.Value != -1 {
						return false, st
					}
				} else if cur != *m.Value {
					return false, st
				}
			}
		}
	}
	return true, next
}

// Partition implements Model.
func (TxnModel) Partition(ops []history.Operation) ([]string, [][]history.Operation) {
	return []string{"txns"}, [][]history.Operation{ops}
}

// Diagnose implements Diagnoser for TxnModel.
func (m TxnModel) Diagnose(ops []history.Operation) ([]int, string, bool) {
	txns := txn.FromOperations(ops)
	// The search checks strict serializability (Step applies transactions
	// atomically in real-time order), so the fast path must not claim more:
	// strict is the level whose anomalies are all proofs against it.
	rep := txn.Check(txns, txn.LevelStrictSerializable)
	if rep.Verdict != txn.Violation || len(rep.Anomalies) == 0 {
		return nil, "", false
	}
	first := rep.Anomalies[0]
	var witness []int
	if first.Cycle != nil {
		for _, nodeID := range first.Cycle.Nodes {
			for i, o := range ops {
				if o.ID == nodeID {
					witness = append(witness, i)
					break
				}
			}
		}
	} else if len(first.Txns) > 0 {
		for _, t := range first.Txns {
			for i, o := range ops {
				if o.ID == t.ID {
					witness = append(witness, i)
					break
				}
			}
		}
	}
	seen := make(map[int]bool)
	var dedup []int
	for _, w := range witness {
		if !seen[w] {
			seen[w] = true
			dedup = append(dedup, w)
		}
	}
	sort.Ints(dedup)
	explanation := first.String()
	return dedup, explanation, true
}
