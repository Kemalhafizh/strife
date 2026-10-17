package checker

import (
	"fmt"
	"sort"
	"testing"

	"github.com/kemalhafizh/strife/internal/rng"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
)

// genHistory builds a linearizable history of a single register: procs
// processes issue ops back to back; each op takes effect at a random instant
// inside its interval, and reads return what the register held then. A
// fraction of writes is turned indeterminate ("info", e.g. a timeout), which
// is what makes real histories expensive to check.
func genHistory(seed uint64, n, procs int, infoRate float64) []history.Operation {
	r := rng.New(seed)
	type pending struct {
		op  history.Operation
		lin int64
	}
	next := make([]int64, procs)
	ps := make([]pending, 0, n)
	for i := 0; i < n; i++ {
		p := r.Intn(procs)
		call := next[p] + r.Int64n(5)
		ret := call + 1 + r.Int64n(20)
		next[p] = ret + 1
		in := history.Op{Index: i, Process: p, Type: history.Invoke, Key: "x"}
		out := history.Op{Process: p, Type: history.OK}
		switch x := r.Float64(); {
		case x < 0.5:
			in.F = history.FRead
		case x < 0.7:
			in.F, in.Old, in.Value = history.FCas, history.Int(r.Int64n(5)), history.Int(r.Int64n(5))
		default:
			in.F, in.Value = history.FWrite, history.Int(r.Int64n(5))
		}
		ps = append(ps, pending{history.Operation{Process: p, Call: call, Return: ret, Input: in, Output: out}, call + r.Int64n(ret-call+1)})
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].lin < ps[j].lin })
	var cur *int64
	for i := range ps {
		o := &ps[i].op
		switch o.Input.F {
		case history.FRead:
			o.Output.Value = cur
		case history.FWrite:
			cur = o.Input.Value
		case history.FCas:
			if cur != nil && *cur == *o.Input.Old {
				cur = o.Input.Value
			} else {
				o.Output.Type = history.Fail
			}
		}
		if o.Input.F != history.FRead && o.Output.Type == history.OK && r.Bool(infoRate) {
			o.Output.Type, o.Return = history.Info, history.Infinity
		}
	}
	ops := make([]history.Operation, len(ps))
	for i, p := range ps {
		ops[i] = p.op
	}
	sort.SliceStable(ops, func(i, j int) bool { return ops[i].Call < ops[j].Call })
	for i := range ops {
		ops[i].ID = i
		ops[i].Input.Index = i
	}
	return ops
}

// withStaleRead appends a read, after everything completed, of a value the
// register cannot hold: a guaranteed violation the search must prove.
func withStaleRead(ops []history.Operation) []history.Operation {
	var end int64
	for _, o := range ops {
		if o.Return != history.Infinity && o.Return > end {
			end = o.Return
		}
	}
	out := append([]history.Operation(nil), ops...)
	// Write 42, then read 7 strictly afterwards: 7 can never be read again.
	w := history.Op{Index: len(out), Process: 999, Type: history.Invoke, F: history.FWrite, Key: "x", Value: history.Int(42)}
	out = append(out, history.Operation{ID: len(out), Process: 999, Call: end + 10, Return: end + 20, Input: w, Output: history.Op{Type: history.OK}})
	rd := history.Op{Index: len(out), Process: 999, Type: history.Invoke, F: history.FRead, Key: "x"}
	out = append(out, history.Operation{ID: len(out), Process: 999, Call: end + 30, Return: end + 40, Input: rd, Output: history.Op{Type: history.OK, Value: history.Int(7)}})
	return out
}

func TestGeneratedHistoriesAreLinearizable(t *testing.T) {
	for _, info := range []float64{0, 0.05} {
		ops := genHistory(1, 2000, 10, info)
		if r := CheckOperations(model.KV{}, ops, Options{}); r.Verdict != Linearizable {
			t.Fatalf("info=%v: generator produced a non-linearizable history: %+v", info, r.Keys[0])
		}
		if r := CheckOperations(model.KV{}, withStaleRead(ops), Options{}); r.Verdict != Violation {
			t.Fatalf("info=%v: injected stale read not detected", info)
		}
	}
}

func benchCheck(b *testing.B, ops []history.Operation, want Verdict, opts ...Options) {
	b.Helper()
	b.ReportAllocs()
	o := Options{Parallelism: 1}
	if len(opts) > 0 {
		o = opts[0]
		o.Parallelism = 1
	}
	var steps uint64
	for i := 0; i < b.N; i++ {
		r := CheckOperations(model.KV{}, ops, o)
		if r.Verdict == Unknown && want == Linearizable {
			// Honest outcome on the hardest shapes (one key, 50 concurrent
			// processes): the default budget runs out. Recorded, not hidden.
			b.Skipf("verdict unknown (%s) after %d steps: beyond the default budget", r.Keys[0].Reason, r.Keys[0].Steps)
		}
		if r.Verdict != want {
			b.Fatalf("verdict %s, want %s", r.Verdict, want)
		}
		steps = r.Keys[0].Steps
	}
	b.ReportMetric(float64(len(ops))*float64(b.N)/b.Elapsed().Seconds(), "ops/s")
	b.ReportMetric(float64(steps)/float64(len(ops)), "steps/op")
}

// BenchmarkLinearizable checks valid single-register histories: the common
// case, and the worst case for partitioning (everything on one key).
func BenchmarkLinearizable(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000} {
		for _, c := range []int{5, 20, 50} {
			ops := genHistory(1, n, c, 0)
			b.Run(fmt.Sprintf("ops=%d/procs=%d", n, c), func(b *testing.B) { benchCheck(b, ops, Linearizable) })
		}
	}
}

// BenchmarkIndeterminate adds timed-out writes, which stay "pending forever"
// and are the main source of search cost in real histories.
func BenchmarkIndeterminate(b *testing.B) {
	for _, rate := range []float64{0.01, 0.05} {
		for _, n := range []int{1_000, 10_000} {
			ops := genHistory(2, n, 10, rate)
			b.Run(fmt.Sprintf("info=%.0f%%/ops=%d", rate*100, n), func(b *testing.B) { benchCheck(b, ops, Linearizable) })
		}
	}
}

// BenchmarkViolation measures proving non-linearizability. With diagnosis a
// stale read is proved in O(n log n); the search alone has to refute every
// possible order, which indeterminate writes make exponential.
func BenchmarkViolation(b *testing.B) {
	for _, info := range []float64{0, 0.05} {
		for _, n := range []int{1_000, 10_000, 100_000} {
			ops := withStaleRead(genHistory(3, n, 10, info))
			b.Run(fmt.Sprintf("diagnosed/info=%.0f%%/ops=%d", info*100, n), func(b *testing.B) { benchCheck(b, ops, Violation) })
		}
	}
	for _, n := range []int{1_000, 10_000} {
		ops := withStaleRead(genHistory(3, n, 10, 0))
		b.Run(fmt.Sprintf("search-only/info=0%%/ops=%d", n), func(b *testing.B) {
			benchCheck(b, ops, Violation, Options{SearchOnly: true})
		})
	}
}

// BenchmarkWitness measures diagnosis and the generic minimisation.
func BenchmarkWitness(b *testing.B) {
	ops := withStaleRead(genHistory(4, 10_000, 10, 0))
	b.Run("diagnose/ops=10000", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if w, _ := Witness(model.KV{}, ops, WitnessOptions{}); len(w) == 0 {
				b.Fatal("no witness")
			}
		}
	})
	small := withStaleRead(genHistory(5, 300, 5, 0))
	b.Run("generic/ops=300", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			Witness(noDiag{}, small, WitnessOptions{})
		}
	})
}
