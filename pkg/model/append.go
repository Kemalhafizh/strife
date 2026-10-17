package model

import (
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"strings"

	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/txn"
)

// Append is the list-append transactional model: transactions append unique
// values to lists and read whole lists. It is decided by Elle's dependency
// graph analysis (txn.CheckAppend) at the configured isolation level, which
// finds anomalies in O(n log n) instead of searching for a serial order.
// Step implements the strict-serializable sequential specification, used
// only to cross-check the analysis by exhaustive search on small histories.
type Append struct {
	Level string // isolation level, default serializable
}

type appendState struct {
	lists map[string][]int64 // never mutated once shared
}

func (s appendState) keys() []string {
	ks := make([]string, 0, len(s.lists))
	for k := range s.lists {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func (s appendState) Hash() uint64 {
	h := fnv.New64a()
	for _, k := range s.keys() {
		_, _ = fmt.Fprint(h, k, s.lists[k], ";")
	}
	return h.Sum64()
}

func (s appendState) Equal(o State) bool {
	t, ok := o.(appendState)
	if !ok || len(t.lists) != len(s.lists) {
		return false
	}
	for k, v := range s.lists {
		if !slices.Equal(v, t.lists[k]) {
			return false
		}
	}
	return true
}

func (s appendState) String() string {
	var b strings.Builder
	for _, k := range s.keys() {
		fmt.Fprintf(&b, "%s=%v ", k, s.lists[k])
	}
	return strings.TrimSpace(b.String())
}

// Name implements Model.
func (Append) Name() string { return "append" }

// Init implements Model.
func (Append) Init() State { return appendState{lists: map[string][]int64{}} }

// Relevant implements Model. Failed transactions are kept: reading what a
// failed transaction appended is an anomaly (G1a).
func (Append) Relevant(o history.Operation) bool { return o.Input.F == history.FTxn }

// Step implements Model: the transaction's appends apply atomically and its
// reads must return exactly the current lists.
func (Append) Step(s State, in, out history.Op) (bool, State) {
	st := s.(appendState)
	if out.Type == history.Fail {
		return true, st
	}
	micro := in.Txn
	if out.Type == history.OK && len(out.Txn) == len(in.Txn) {
		micro = out.Txn
	}
	next := appendState{lists: make(map[string][]int64, len(st.lists))}
	for k, v := range st.lists {
		next.lists[k] = v
	}
	for _, m := range micro {
		switch m.F {
		case history.MicroAppend:
			if m.Value != nil {
				next.lists[m.Key] = append(slices.Clip(next.lists[m.Key]), *m.Value)
			}
		case history.MicroRead:
			if out.Type == history.OK && !slices.Equal(m.Values, next.lists[m.Key]) {
				return false, st
			}
		}
	}
	return true, next
}

// Partition implements Model: transactions span keys, so there is one.
func (Append) Partition(ops []history.Operation) ([]string, [][]history.Operation) {
	return []string{"txns"}, [][]history.Operation{ops}
}

// Decide implements Decider.
func (m Append) Decide(ops []history.Operation) (bool, []int, string, []string) {
	level := txn.IsolationLevel(m.Level)
	rep := txn.CheckAppend(ops, level)
	if rep.Verdict != txn.Violation {
		return false, nil, "", nil
	}
	var kinds []string
	for _, a := range rep.Anomalies {
		if !slices.Contains(kinds, string(a.Type)) {
			kinds = append(kinds, string(a.Type))
		}
	}
	first := rep.Anomalies[0]
	index := make(map[int]int, len(ops))
	for i, o := range ops {
		index[o.ID] = i
	}
	var witness []int
	add := func(id int) {
		if i, ok := index[id]; ok && !slices.Contains(witness, i) {
			witness = append(witness, i)
		}
	}
	if first.Cycle != nil {
		for _, id := range first.Cycle.Nodes {
			add(id)
		}
	}
	for _, t := range first.Txns {
		add(t.ID)
	}
	sort.Ints(witness)
	diag := fmt.Sprintf("[%s] %s", first.Type, first.Explanation)
	if n := len(rep.Anomalies) - 1; n > 0 {
		counts := map[txn.AnomalyType]int{}
		for _, a := range rep.Anomalies[1:] {
			counts[a.Type]++
		}
		var parts []string
		for t, c := range counts {
			parts = append(parts, fmt.Sprintf("%s×%d", t, c))
		}
		sort.Strings(parts)
		diag += fmt.Sprintf(" (%d more: %s)", n, strings.Join(parts, ", "))
	}
	return true, witness, diag, kinds
}
