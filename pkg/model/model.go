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
