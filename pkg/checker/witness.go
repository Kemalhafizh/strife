package checker

import (
	"fmt"
	"sort"
	"sync"

	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
)

// A violation found by the search is real but rarely readable: the stuck
// operation sits at the end of a linearized prefix hundreds of operations
// long. A witness is the opposite, a handful of operations that already prove
// the history cannot be linearized.
//
// Witnesses must be sound, which rules out naive minimisation: dropping the
// write of 3 from a history makes any read of 3 "unexplainable" without the
// system having done anything wrong. Two sound sources are used:
//
//  1. a model's Diagnoser, which searches the full history for a provable
//     anomaly (for registers: a read of a value that a completed write had
//     already replaced, with no write of that value able to come between);
//  2. otherwise, delta debugging that only removes pure reads. A read never
//     changes state, so removing reads can only remove constraints: if the
//     reduced history still has no linearization, neither has the original.

// WitnessOptions bound the generic minimisation of one violating key.
type WitnessOptions struct {
	// MaxChecks bounds oracle calls per key; 0 means 4000.
	MaxChecks int
	// MaxSteps bounds each oracle call's search; 0 means 1 million.
	MaxSteps uint64
	// MaxSize is the largest generic witness worth reporting; 0 means 12.
	MaxSize int
	// DiagnoseOnly skips the generic minimisation and only asks the model
	// for a diagnosis, which is cheap enough to run on every explored seed.
	DiagnoseOnly bool
}

// AddWitnesses attaches a witness and a diagnosis to the counterexample of
// every violating key of rep, which must be the result of checking ops with
// m. It is deterministic.
func AddWitnesses(m model.Model, ops []history.Op, rep *Report, o WitnessOptions) error {
	if rep.Verdict != Violation {
		return nil
	}
	paired, err := history.Pair(ops)
	if err != nil {
		return err
	}
	keys, parts := partitions(m, paired)
	byKey := map[string][]history.Operation{}
	for i, k := range keys {
		byKey[k] = parts[i]
	}
	var wg sync.WaitGroup
	for i := range rep.Keys {
		kr := &rep.Keys[i]
		if kr.Verdict != Violation || kr.Counterexample == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, diag := Witness(m, byKey[kr.Key], o)
			kr.Counterexample.Witness = nil
			for _, op := range w {
				kr.Counterexample.Witness = append(kr.Counterexample.Witness, step(op))
			}
			kr.Counterexample.Diagnosis = diag
		}()
	}
	wg.Wait()
	return nil
}

func partitions(m model.Model, ops []history.Operation) ([]string, [][]history.Operation) {
	var relevant []history.Operation
	for _, o := range ops {
		if m.Relevant(o) {
			relevant = append(relevant, o)
		}
	}
	return m.Partition(relevant)
}

const defaultWitnessSteps = 1_000_000

// Witness returns a small set of operations of a violating partition that
// proves it is not linearizable, with a diagnosis, or nil if none was found
// within the budget.
func Witness(m model.Model, part []history.Operation, o WitnessOptions) ([]history.Operation, string) {
	if d, ok := m.(model.Decider); ok {
		// The decision procedure already names the anomaly and its
		// transactions; searching would be both slow and pointless.
		bad, idx, diag, _ := d.Decide(part)
		if !bad {
			return nil, ""
		}
		w := make([]history.Operation, len(idx))
		for i, j := range idx {
			w[i] = part[j]
		}
		return w, diag
	}
	if d, ok := m.(model.Diagnoser); ok {
		if idx, diag, ok := d.Diagnose(part); ok {
			w := make([]history.Operation, len(idx))
			for i, j := range idx {
				w[i] = part[j]
			}
			return w, diag
		}
	}
	if o.DiagnoseOnly {
		return nil, ""
	}
	if o.MaxChecks <= 0 {
		o.MaxChecks = 4000
	}
	if o.MaxSteps == 0 {
		o.MaxSteps = defaultWitnessSteps
	}
	if o.MaxSize <= 0 {
		o.MaxSize = 12
	}
	var fixed, reads []history.Operation
	for _, op := range part {
		if op.Input.F == history.FRead {
			reads = append(reads, op)
		} else {
			fixed = append(fixed, op)
		}
	}
	budget := o.MaxChecks
	fails := func(sub []history.Operation) bool {
		if budget <= 0 {
			return false
		}
		budget--
		all := append(append([]history.Operation(nil), fixed...), sub...)
		return checkPartition(m, all, o.MaxSteps, 0).Verdict == Violation
	}
	if !fails(reads) {
		return nil, ""
	}
	min := ddmin(reads, fails)
	if len(fixed)+len(min) > o.MaxSize {
		return nil, ""
	}
	w := append(append([]history.Operation(nil), fixed...), min...)
	sort.Slice(w, func(i, j int) bool { return w[i].Call < w[j].Call })
	return w, fmt.Sprintf("These %d operations alone cannot be ordered in any way that respects both real time and the model's semantics.", len(w))
}

// ddmin is Zeller & Hildebrandt's delta debugging minimisation: it returns a
// subset of items for which fails holds and from which no single chunk at
// the final granularity can be removed. With granularity grown to one item,
// the result is 1-minimal.
func ddmin[T any](items []T, fails func([]T) bool) []T {
	n := 2
	for len(items) >= 2 {
		chunks := chunk(items, n)
		reduced := false
		for _, c := range chunks {
			if fails(c) {
				items, n, reduced = c, 2, true
				break
			}
		}
		if !reduced && len(chunks) > 2 {
			for i := range chunks {
				comp := without(chunks, i)
				if fails(comp) {
					items, n, reduced = comp, max(n-1, 2), true
					break
				}
			}
		}
		if reduced {
			continue
		}
		if n >= len(items) {
			break
		}
		n = min(2*n, len(items))
	}
	return items
}

func chunk[T any](items []T, n int) [][]T {
	var out [][]T
	size, rem := len(items)/n, len(items)%n
	at := 0
	for i := 0; i < n; i++ {
		k := size
		if i < rem {
			k++
		}
		if k == 0 {
			continue
		}
		out = append(out, items[at:at+k])
		at += k
	}
	return out
}

func without[T any](chunks [][]T, skip int) []T {
	var out []T
	for i, c := range chunks {
		if i != skip {
			out = append(out, c...)
		}
	}
	return out
}
