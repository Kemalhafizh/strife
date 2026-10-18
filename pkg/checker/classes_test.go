package checker

import (
	"os"
	"strconv"
	"testing"

	"github.com/kemalhafizh/strife/internal/rng"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
)

// The search tries one operation per class of interchangeable candidates
// (same input, same output). These cross-checks against brute force cover
// every model with small value domains, where such twins are common.

func bruteIters(def int) int {
	if v, err := strconv.Atoi(os.Getenv("STRIFE_BRUTE_ITERS")); err == nil && v > 0 {
		return v
	}
	return def
}

func randomOps(r *rng.Rand, n int, gen func(i int, in, out *history.Op)) []history.Operation {
	var ops []history.Operation
	for i := 0; i < n; i++ {
		call := r.Int64n(16)
		ret := call + 1 + r.Int64n(8)
		in := history.Op{Index: i, Process: i, Type: history.Invoke, Key: "x"}
		out := history.Op{Process: i, Type: history.OK, Key: "x"}
		gen(i, &in, &out)
		if out.Type == history.Info {
			ret = history.Infinity
		}
		ops = append(ops, history.Operation{ID: i, Process: i, Call: call, Return: ret, Input: in, Output: out})
	}
	return ops
}

func crossCheck(t *testing.T, name string, m model.Model, gen func(r *rng.Rand) []history.Operation) {
	t.Helper()
	r := rng.New(77)
	agree := map[bool]int{}
	for iter := 0; iter < bruteIters(4000); iter++ {
		ops := gen(r)
		var rel []history.Operation
		for _, o := range ops {
			if m.Relevant(o) {
				rel = append(rel, o)
			}
		}
		want := bruteForce(m, rel)
		got := CheckOperations(m, ops, Options{Parallelism: 1, SearchOnly: true})
		if (got.Verdict == Linearizable) != want {
			t.Fatalf("%s iter %d: search=%s brute=%v\n%s", name, iter, got.Verdict, want, dump(rel))
		}
		agree[want]++
	}
	if agree[true] < 200 || agree[false] < 200 {
		t.Fatalf("%s: random histories not diverse enough: %v", name, agree)
	}
}

func TestInterchangeableCandidatesKV(t *testing.T) {
	crossCheck(t, "kv", model.KV{}, func(r *rng.Rand) []history.Operation {
		return randomOps(r, 2+r.Intn(7), func(_ int, in, out *history.Op) {
			switch r.Intn(3) {
			case 0:
				in.F, in.Value = history.FWrite, v(r.Int64n(2))
			case 1:
				in.F = history.FRead
				if r.Bool(0.8) {
					out.Value = v(r.Int64n(2))
				}
			default:
				in.F, in.Old, in.Value = history.FCas, v(r.Int64n(2)), v(r.Int64n(2))
			}
			if in.F != history.FRead && r.Bool(0.15) {
				out.Type = history.Info
			}
		})
	})
}

func TestInterchangeableCandidatesCounter(t *testing.T) {
	crossCheck(t, "counter", model.Counter{}, func(r *rng.Rand) []history.Operation {
		return randomOps(r, 2+r.Intn(7), func(_ int, in, out *history.Op) {
			if r.Bool(0.5) {
				in.F, in.Value = history.FAdd, v(1+r.Int64n(2))
				if r.Bool(0.15) {
					out.Type = history.Info
				}
			} else {
				in.F, out.Value = history.FRead, v(r.Int64n(5))
			}
		})
	})
}

func TestInterchangeableCandidatesQueue(t *testing.T) {
	crossCheck(t, "queue", model.Queue{}, func(r *rng.Rand) []history.Operation {
		return randomOps(r, 2+r.Intn(7), func(_ int, in, out *history.Op) {
			if r.Bool(0.5) {
				in.F, in.Value = history.FEnqueue, v(r.Int64n(2))
				if r.Bool(0.15) {
					out.Type = history.Info
				}
			} else {
				in.F = history.FDequeue
				if r.Bool(0.8) {
					out.Value = v(r.Int64n(2))
				}
			}
		})
	})
}

func TestInterchangeableCandidatesSet(t *testing.T) {
	crossCheck(t, "set", model.Set{}, func(r *rng.Rand) []history.Operation {
		return randomOps(r, 2+r.Intn(7), func(_ int, in, out *history.Op) {
			if r.Bool(0.5) {
				in.F, in.Value = history.FAdd, v(r.Int64n(3))
				if r.Bool(0.15) {
					out.Type = history.Info
				}
			} else {
				in.F = history.FRead
				for x := int64(0); x < 3; x++ {
					if r.Bool(0.5) {
						out.Values = append(out.Values, x)
					}
				}
			}
		})
	})
}
