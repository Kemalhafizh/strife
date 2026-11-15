package checker

import (
	"fmt"
	"slices"
	"testing"

	"github.com/kemalhafizh/strife/internal/rng"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
)

// builder makes hand written histories readable.
type builder struct {
	ops []history.Op
	t   int64
}

func (b *builder) add(op history.Op) *builder {
	b.t++
	op.Index = len(b.ops)
	op.Time = b.t
	b.ops = append(b.ops, op)
	return b
}

func inv(p int, f, k string, v *int64) history.Op {
	return history.Op{Process: p, Type: history.Invoke, F: f, Key: k, Value: v}
}

func done(p int, t history.Type, f, k string, v *int64) history.Op {
	return history.Op{Process: p, Type: t, F: f, Key: k, Value: v}
}

func cas(p int, k string, old, nw int64) history.Op {
	return history.Op{Process: p, Type: history.Invoke, F: history.FCas, Key: k, Old: history.Int(old), Value: history.Int(nw)}
}

var v = history.Int

func mustCheck(t *testing.T, ops []history.Op) Report {
	t.Helper()
	r, err := Check(model.KV{}, ops, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSequentialOK(t *testing.T) {
	b := &builder{}
	b.add(inv(0, "write", "x", v(1))).add(done(0, history.OK, "write", "x", v(1)))
	b.add(inv(0, "read", "x", nil)).add(done(0, history.OK, "read", "x", v(1)))
	b.add(cas(0, "x", 1, 2)).add(done(0, history.OK, "cas", "x", nil))
	b.add(inv(0, "read", "x", nil)).add(done(0, history.OK, "read", "x", v(2)))
	if r := mustCheck(t, b.ops); r.Verdict != Linearizable {
		t.Fatalf("got %s", r.Verdict)
	}
}

func TestStaleReadIsViolation(t *testing.T) {
	b := &builder{}
	b.add(inv(0, "write", "x", v(1))).add(done(0, history.OK, "write", "x", v(1)))
	b.add(inv(1, "write", "x", v(2))).add(done(1, history.OK, "write", "x", v(2)))
	b.add(inv(2, "read", "x", nil)).add(done(2, history.OK, "read", "x", v(1)))
	r := mustCheck(t, b.ops)
	if r.Verdict != Violation {
		t.Fatalf("got %s", r.Verdict)
	}
	ce := r.Keys[0].Counterexample
	if ce == nil || ce.Stuck.Process != 2 {
		t.Fatalf("bad counterexample: %+v", ce)
	}
}

func TestConcurrentReadMaySeeEither(t *testing.T) {
	b := &builder{}
	b.add(inv(0, "write", "x", v(1))).add(done(0, history.OK, "write", "x", v(1)))
	b.add(inv(1, "write", "x", v(2)))
	b.add(inv(2, "read", "x", nil)).add(done(2, history.OK, "read", "x", v(1)))
	b.add(inv(3, "read", "x", nil)).add(done(3, history.OK, "read", "x", v(2)))
	b.add(done(1, history.OK, "write", "x", v(2)))
	if r := mustCheck(t, b.ops); r.Verdict != Linearizable {
		t.Fatalf("got %s", r.Verdict)
	}
}

func TestNewOldInversion(t *testing.T) {
	// Two sequential reads concurrent with one write: once a reader has seen
	// the new value, a later read must not see the old one.
	b := &builder{}
	b.add(inv(0, "write", "x", v(1))).add(done(0, history.OK, "write", "x", v(1)))
	b.add(inv(1, "write", "x", v(2)))
	b.add(inv(2, "read", "x", nil)).add(done(2, history.OK, "read", "x", v(2)))
	b.add(inv(3, "read", "x", nil)).add(done(3, history.OK, "read", "x", v(1)))
	b.add(done(1, history.OK, "write", "x", v(2)))
	if r := mustCheck(t, b.ops); r.Verdict != Violation {
		t.Fatalf("got %s", r.Verdict)
	}
}

func TestInfoWriteMayOrMayNotHappen(t *testing.T) {
	for _, seen := range []int64{0, 9} {
		b := &builder{}
		b.add(inv(0, "write", "x", v(0))).add(done(0, history.OK, "write", "x", v(0)))
		b.add(inv(1, "write", "x", v(9))).add(done(1, history.Info, "write", "x", nil))
		b.add(inv(2, "read", "x", nil)).add(done(2, history.OK, "read", "x", v(seen)))
		if r := mustCheck(t, b.ops); r.Verdict != Linearizable {
			t.Fatalf("read %d: got %s", seen, r.Verdict)
		}
	}
	// ...but once seen, it cannot be un-seen.
	b := &builder{}
	b.add(inv(0, "write", "x", v(0))).add(done(0, history.OK, "write", "x", v(0)))
	b.add(inv(1, "write", "x", v(9))).add(done(1, history.Info, "write", "x", nil))
	b.add(inv(2, "read", "x", nil)).add(done(2, history.OK, "read", "x", v(9)))
	b.add(inv(2, "read", "x", nil)).add(done(2, history.OK, "read", "x", v(0)))
	if r := mustCheck(t, b.ops); r.Verdict != Violation {
		t.Fatalf("got %s", r.Verdict)
	}
}

func TestFailedOpsIgnored(t *testing.T) {
	b := &builder{}
	b.add(inv(0, "write", "x", v(1))).add(done(0, history.OK, "write", "x", v(1)))
	b.add(inv(1, "write", "x", v(5))).add(done(1, history.Fail, "write", "x", nil))
	b.add(inv(2, "read", "x", nil)).add(done(2, history.OK, "read", "x", v(1)))
	if r := mustCheck(t, b.ops); r.Verdict != Linearizable {
		t.Fatalf("got %s", r.Verdict)
	}
}

func TestCasSucceedingOnWrongValue(t *testing.T) {
	b := &builder{}
	b.add(inv(0, "write", "x", v(1))).add(done(0, history.OK, "write", "x", v(1)))
	b.add(cas(0, "x", 3, 4)).add(done(0, history.OK, "cas", "x", nil))
	if r := mustCheck(t, b.ops); r.Verdict != Violation {
		t.Fatalf("got %s", r.Verdict)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	b := &builder{}
	b.add(inv(0, "write", "a", v(1))).add(done(0, history.OK, "write", "a", v(1)))
	b.add(inv(0, "write", "b", v(2))).add(done(0, history.OK, "write", "b", v(2)))
	b.add(inv(1, "read", "a", nil)).add(done(1, history.OK, "read", "a", v(1)))
	b.add(inv(1, "read", "b", nil)).add(done(1, history.OK, "read", "b", nil))
	r := mustCheck(t, b.ops)
	if r.Verdict != Violation || len(r.Keys) != 2 || r.Keys[0].Verdict != Linearizable || r.Keys[1].Verdict != Violation {
		t.Fatalf("got %+v", r)
	}
}

func TestSetModel(t *testing.T) {
	b := &builder{}
	b.add(history.Op{Process: 0, Type: history.Invoke, F: "add", Value: v(1)}).add(history.Op{Process: 0, Type: history.OK, F: "add", Value: v(1)})
	b.add(history.Op{Process: 1, Type: history.Invoke, F: "add", Value: v(2)})
	b.add(history.Op{Process: 2, Type: history.Invoke, F: "read"}).add(history.Op{Process: 2, Type: history.OK, F: "read", Values: []int64{1}})
	b.add(history.Op{Process: 1, Type: history.OK, F: "add", Value: v(2)})
	b.add(history.Op{Process: 2, Type: history.Invoke, F: "read"}).add(history.Op{Process: 2, Type: history.OK, F: "read", Values: []int64{2, 1}})
	r, err := Check(model.Set{}, b.ops, Options{})
	if err != nil || r.Verdict != Linearizable {
		t.Fatalf("got %v %v", r.Verdict, err)
	}
	b.add(history.Op{Process: 2, Type: history.Invoke, F: "read"}).add(history.Op{Process: 2, Type: history.OK, F: "read", Values: []int64{2}})
	r, _ = Check(model.Set{}, b.ops, Options{})
	if r.Verdict != Violation {
		t.Fatalf("lost element not detected: %v", r.Verdict)
	}
}

// TestAgainstBruteForce cross-checks the WGL search with an exhaustive
// permutation search on many small random histories.
func TestAgainstBruteForce(t *testing.T) {
	r := rng.New(2024)
	agree := map[Verdict]int{}
	for iter := 0; iter < 3000; iter++ {
		ops := randomHistory(r, 1+r.Intn(6))
		got := CheckOperations(model.KV{}, ops, Options{Parallelism: 1})
		search := CheckOperations(model.KV{}, ops, Options{Parallelism: 1, SearchOnly: true})
		want := bruteForce(model.KV{}, ops)
		if (got.Verdict == Linearizable) != want || (search.Verdict == Linearizable) != want {
			t.Fatalf("iter %d: checker=%s search-only=%s brute=%v\n%s", iter, got.Verdict, search.Verdict, want, dump(ops))
		}
		agree[got.Verdict]++
	}
	if agree[Linearizable] < 300 || agree[Violation] < 300 {
		t.Fatalf("random histories not diverse enough: %v", agree)
	}
}

func randomHistory(r *rng.Rand, n int) []history.Operation {
	var ops []history.Operation
	for i := 0; i < n; i++ {
		call := r.Int64n(20)
		ret := call + 1 + r.Int64n(8)
		in := history.Op{Index: i, Process: i, Type: history.Invoke, Key: "x"}
		out := history.Op{Process: i, Type: history.OK, Key: "x"}
		switch r.Intn(3) {
		case 0:
			in.F, in.Value = history.FWrite, v(r.Int64n(3))
		case 1:
			in.F = history.FRead
			if r.Bool(0.8) {
				out.Value = v(r.Int64n(3))
			}
		case 2:
			in.F, in.Old, in.Value = history.FCas, v(r.Int64n(3)), v(r.Int64n(3))
		}
		if in.F != history.FRead && r.Bool(0.15) {
			out.Type, ret = history.Info, history.Infinity
		}
		ops = append(ops, history.Operation{ID: i, Process: i, Call: call, Return: ret, Input: in, Output: out})
	}
	return ops
}

func bruteForce(m model.Model, ops []history.Operation) bool {
	n := len(ops)
	used := make([]bool, n)
	var rec func(s model.State, depth int) bool
	rec = func(s model.State, depth int) bool {
		if depth == n {
			return true
		}
		for i := 0; i < n; i++ {
			if used[i] {
				continue
			}
			// i may go next only if no other unused op returned before i was called.
			ok := true
			for j := 0; j < n; j++ {
				if !used[j] && j != i && ops[j].Return < ops[i].Call {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			good, ns := m.Step(s, ops[i].Input, ops[i].Output)
			if !good {
				continue
			}
			used[i] = true
			if rec(ns, depth+1) {
				return true
			}
			used[i] = false
		}
		return false
	}
	return rec(m.Init(), 0)
}

func dump(ops []history.Operation) string {
	s := ""
	for _, o := range ops {
		s += fmt.Sprintf("  [%d,%d] %s\n", o.Call, o.Return, o.Describe())
	}
	return s
}

func BenchmarkCheck(b *testing.B) {
	r := rng.New(1)
	var ops []history.Operation
	// A linearizable history: an atomic register with overlapping clients.
	var cur int64
	for i := 0; i < 400; i++ {
		call := int64(i * 10)
		in := history.Op{Index: i, Process: i % 8, Type: history.Invoke, Key: "x", F: history.FWrite}
		out := history.Op{Process: i % 8, Type: history.OK}
		if r.Bool(0.5) {
			in.F = history.FRead
			out.Value = v(cur)
		} else {
			cur = r.Int64n(5)
			in.Value = v(cur)
		}
		ops = append(ops, history.Operation{ID: i, Process: i % 8, Call: call, Return: call + 5 + r.Int64n(30), Input: in, Output: out})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CheckOperations(model.KV{}, ops, Options{})
	}
}

// generateConcurrentKVHistory constructs a genuinely concurrent single-key
// linearizable history of n operations across procs processes where invocation
// order (Call) is scrambled relative to the true server linearization order.
func generateConcurrentKVHistory(seed uint64, n, procs int, numValues int64) []history.Operation {
	r := rng.New(seed)
	ops := make([]history.Operation, 0, n)
	procFreeAt := make([]int64, procs)
	var cur int64
	var set bool
	var serverTime int64 = 100

	for i := 0; i < n; i++ {
		p := i % procs
		serverTime += 1 + r.Int64n(5)
		if procFreeAt[p]+2 > serverTime {
			serverTime = procFreeAt[p] + 2
		}
		// Call happened before serverTime (after process p became free);
		// Return happens after serverTime, creating up to procs-way overlap.
		maxLead := serverTime - procFreeAt[p] - 1
		if maxLead > int64(procs)*4 {
			maxLead = int64(procs) * 4
		}
		lead := int64(1)
		if maxLead > 1 {
			lead += r.Int64n(maxLead)
		}
		call := serverTime - lead
		ret := serverTime + 1 + r.Int64n(int64(procs)*4)
		procFreeAt[p] = ret + 1

		in := history.Op{Index: i, Process: p, Type: history.Invoke, Key: "x"}
		out := history.Op{Process: p, Type: history.OK, Key: "x"}
		roll := r.Intn(10)
		switch {
		case roll < 5: // 50% reads
			in.F = history.FRead
			if set {
				out.Value = v(cur)
			}
		case roll < 8: // 30% writes
			in.F = history.FWrite
			cur = r.Int64n(numValues)
			set = true
			in.Value = v(cur)
		default: // 20% CAS
			in.F = history.FCas
			if set {
				in.Old = v(cur)
			}
			cur = r.Int64n(numValues)
			set = true
			in.Value = v(cur)
		}
		ops = append(ops, history.Operation{
			ID:      i,
			Process: p,
			Call:    call,
			Return:  ret,
			Input:   in,
			Output:  out,
		})
	}
	return ops
}

func TestCheck50ConcurrentProcesses(t *testing.T) {
	for _, n := range []int{500, 1000, 2000, 5000, 10000} {
		ops := generateConcurrentKVHistory(2026, n, 50, 64)
		rep := CheckOperations(model.KV{}, ops, Options{SearchOnly: true, MaxSteps: 100_000})
		if rep.Verdict != Linearizable {
			t.Fatalf("n=%d: expected linearizable, got %s (steps=%d, reason=%s)", n, rep.Verdict, rep.Keys[0].Steps, rep.Keys[0].Reason)
		}
	}
}

func BenchmarkCheck50Concurrent(b *testing.B) {
	ops := generateConcurrentKVHistory(2026, 10_000, 50, 64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rep := CheckOperations(model.KV{}, ops, Options{})
		if rep.Verdict != Linearizable {
			b.Fatalf("unexpected verdict: %s", rep.Verdict)
		}
	}
}

func TestQueueModelLinearizable(t *testing.T) {
	b := &builder{}
	b.add(inv(0, "enqueue", "q", v(10))).add(done(0, history.OK, "enqueue", "q", v(10)))
	b.add(inv(1, "enqueue", "q", v(20))).add(done(1, history.OK, "enqueue", "q", v(20)))
	b.add(inv(2, "dequeue", "q", nil)).add(done(2, history.OK, "dequeue", "q", v(10)))
	b.add(inv(3, "dequeue", "q", nil)).add(done(3, history.OK, "dequeue", "q", v(20)))
	b.add(inv(4, "dequeue", "q", nil)).add(done(4, history.OK, "dequeue", "q", nil))

	r, err := Check(model.Queue{}, b.ops, Options{})
	if err != nil || r.Verdict != Linearizable {
		t.Fatalf("expected linearizable queue, got %s (err: %v)", r.Verdict, err)
	}
}

func TestQueueFIFOInversionViolation(t *testing.T) {
	b := &builder{}
	// Enqueue 10 completes strictly before enqueue 20 begins
	b.add(inv(0, "enqueue", "q", v(10))).add(done(0, history.OK, "enqueue", "q", v(10)))
	b.add(inv(1, "enqueue", "q", v(20))).add(done(1, history.OK, "enqueue", "q", v(20)))
	// Dequeue returns 20 instead of 10
	b.add(inv(2, "dequeue", "q", nil)).add(done(2, history.OK, "dequeue", "q", v(20)))

	r, err := Check(model.Queue{}, b.ops, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != Violation {
		t.Fatalf("expected violation for FIFO order inversion, got %s", r.Verdict)
	}
}

func TestQueuePhantomDequeueViolation(t *testing.T) {
	b := &builder{}
	b.add(inv(0, "enqueue", "q", v(10))).add(done(0, history.OK, "enqueue", "q", v(10)))
	// Dequeue returns 999 which was never enqueued
	b.add(inv(1, "dequeue", "q", nil)).add(done(1, history.OK, "dequeue", "q", v(999)))

	r, err := Check(model.Queue{}, b.ops, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != Violation {
		t.Fatalf("expected violation for phantom dequeue, got %s", r.Verdict)
	}
}

func TestCounterModelLinearizable(t *testing.T) {
	b := &builder{}
	b.add(inv(0, "add", "c", v(2))).add(done(0, history.OK, "add", "c", v(2)))
	b.add(inv(1, "inc", "c", nil)).add(done(1, history.OK, "inc", "c", nil))
	b.add(inv(2, "read", "c", nil)).add(done(2, history.OK, "read", "c", v(3)))

	r, err := Check(model.Counter{}, b.ops, Options{})
	if err != nil || r.Verdict != Linearizable {
		t.Fatalf("expected linearizable counter, got %s (err: %v)", r.Verdict, err)
	}
}

func TestCounterStaleReadViolation(t *testing.T) {
	b := &builder{}
	b.add(inv(0, "add", "c", v(5))).add(done(0, history.OK, "add", "c", v(5)))
	b.add(inv(1, "add", "c", v(3))).add(done(1, history.OK, "add", "c", v(3)))
	// Both completed, so counter >= 8. Reading 5 is stale.
	b.add(inv(2, "read", "c", nil)).add(done(2, history.OK, "read", "c", v(5)))

	r, err := Check(model.Counter{}, b.ops, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != Violation {
		t.Fatalf("expected violation for stale counter read, got %s", r.Verdict)
	}
}

func TestIDGenDuplicateViolation(t *testing.T) {
	b := &builder{}
	b.add(inv(0, "generate", "id", nil)).add(done(0, history.OK, "generate", "id", v(42)))
	b.add(inv(1, "generate", "id", nil)).add(done(1, history.OK, "generate", "id", v(42)))

	r, err := Check(model.IDGen{}, b.ops, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != Violation {
		t.Fatalf("expected violation for duplicate generated ID, got %s", r.Verdict)
	}
}

func TestTxnModelIntegration(t *testing.T) {
	b := &builder{}
	// T0: writes x=1, y=1
	b.add(history.Op{
		Process: 0,
		Type:    history.Invoke,
		F:       history.FTxn,
		Txn: []history.MicroOp{
			{F: "w", Key: "x", Value: v(1)},
			{F: "w", Key: "y", Value: v(1)},
		},
	}).add(history.Op{Process: 0, Type: history.OK, F: history.FTxn})

	// T1: reads x=1, reads y=1, writes x=0
	b.add(history.Op{
		Process: 1,
		Type:    history.Invoke,
		F:       history.FTxn,
		Txn: []history.MicroOp{
			{F: "r", Key: "x", Value: v(1)},
			{F: "r", Key: "y", Value: v(1)},
			{F: "w", Key: "x", Value: v(0)},
		},
	}).add(history.Op{Process: 1, Type: history.OK, F: history.FTxn})

	// T2: reads x=1, reads y=1, writes y=0 (Write Skew concurrent with T1)
	b.add(history.Op{
		Process: 2,
		Type:    history.Invoke,
		F:       history.FTxn,
		Txn: []history.MicroOp{
			{F: "r", Key: "x", Value: v(1)},
			{F: "r", Key: "y", Value: v(1)},
			{F: "w", Key: "y", Value: v(0)},
		},
	}).add(history.Op{Process: 2, Type: history.OK, F: history.FTxn})

	r, err := Check(model.TxnModel{Level: "serializable"}, b.ops, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != Violation {
		t.Fatalf("expected violation for transactional write skew, got %s", r.Verdict)
	}
	if len(r.Keys) == 0 || r.Keys[0].Counterexample == nil {
		t.Fatal("expected counterexample with diagnosis")
	}
	if r.Keys[0].Counterexample.Diagnosis == "" {
		t.Fatal("expected non-empty diagnosis for transactional violation")
	}
}

func randomSetHistory(r *rng.Rand, n int) []history.Operation {
	var ops []history.Operation
	for i := 0; i < n; i++ {
		call := r.Int64n(16)
		ret := call + 1 + r.Int64n(6)
		in := history.Op{Index: i, Process: i, Type: history.Invoke}
		out := history.Op{Process: i, Type: history.OK}
		if r.Bool(0.55) {
			in.F, in.Value = "add", v(1+r.Int64n(3))
			out.F, out.Value = "add", in.Value
		} else {
			in.F, out.F = "read", "read"
			k := r.Intn(3)
			for j := 0; j < k; j++ {
				out.Values = append(out.Values, 1+r.Int64n(3))
			}
		}
		if in.F == "add" && r.Bool(0.15) {
			out.Type, ret = history.Info, history.Infinity
		}
		ops = append(ops, history.Operation{ID: i, Process: i, Call: call, Return: ret, Input: in, Output: out})
	}
	return ops
}

func randomCounterHistory(r *rng.Rand, n int) []history.Operation {
	var ops []history.Operation
	for i := 0; i < n; i++ {
		call := r.Int64n(16)
		ret := call + 1 + r.Int64n(6)
		in := history.Op{Index: i, Process: i, Type: history.Invoke, Key: "c"}
		out := history.Op{Process: i, Type: history.OK, Key: "c"}
		switch r.Intn(3) {
		case 0:
			in.F, out.F = "add", "add"
			delta := 1 + r.Int64n(2)
			in.Value = v(delta)
			out.Value = v(delta)
		case 1:
			in.F, out.F = "inc", "inc"
		default:
			in.F, out.F = "read", "read"
			out.Value = v(r.Int64n(5))
		}
		if in.F != "read" && r.Bool(0.15) {
			out.Type, ret = history.Info, history.Infinity
		}
		ops = append(ops, history.Operation{ID: i, Process: i, Call: call, Return: ret, Input: in, Output: out})
	}
	return ops
}

func randomQueueHistory(r *rng.Rand, n int) []history.Operation {
	var ops []history.Operation
	for i := 0; i < n; i++ {
		call := r.Int64n(16)
		ret := call + 1 + r.Int64n(6)
		in := history.Op{Index: i, Process: i, Type: history.Invoke, Key: "q"}
		out := history.Op{Process: i, Type: history.OK, Key: "q"}
		if r.Bool(0.55) {
			in.F, out.F = "enqueue", "enqueue"
			in.Value = v(1 + r.Int64n(3))
			out.Value = in.Value
		} else {
			in.F, out.F = "dequeue", "dequeue"
			if r.Bool(0.75) {
				out.Value = v(1 + r.Int64n(3))
			}
		}
		if in.F == "enqueue" && r.Bool(0.1) {
			out.Type, ret = history.Info, history.Infinity
		}
		ops = append(ops, history.Operation{ID: i, Process: i, Call: call, Return: ret, Input: in, Output: out})
	}
	return ops
}

func randomIDGenHistory(r *rng.Rand, n int) []history.Operation {
	var ops []history.Operation
	for i := 0; i < n; i++ {
		call := r.Int64n(16)
		ret := call + 1 + r.Int64n(6)
		in := history.Op{Index: i, Process: i, Type: history.Invoke, Key: "id", F: "generate"}
		out := history.Op{Process: i, Type: history.OK, Key: "id", F: "generate", Value: v(1 + r.Int64n(4))}
		if r.Bool(0.15) {
			out.Type, ret = history.Info, history.Infinity
			out.Value = nil
		}
		ops = append(ops, history.Operation{ID: i, Process: i, Call: call, Return: ret, Input: in, Output: out})
	}
	return ops
}

func randomAppendHistory(r *rng.Rand, n int, uniqueVal *int64) []history.Operation {
	var ops []history.Operation
	keys := []string{"k0", "k1"}
	seenByKey := map[string][]int64{}
	infoCount := 0
	for i := 0; i < n; i++ {
		call := r.Int64n(16)
		ret := call + 1 + r.Int64n(6)
		k := keys[r.Intn(len(keys))]
		var micro []history.MicroOp
		hasRead := r.Bool(0.5)
		hasAppend := r.Bool(0.6) || !hasRead
		if hasAppend {
			*uniqueVal++
			val := *uniqueVal
			seenByKey[k] = append(seenByKey[k], val)
			micro = append(micro, history.MicroOp{F: history.MicroAppend, Key: k, Value: v(val)})
		}
		if hasRead {
			pool := seenByKey[k]
			var vs []int64
			if len(pool) > 0 && r.Bool(0.8) {
				start := 0
				end := 1 + r.Intn(len(pool))
				if r.Bool(0.3) && end > start {
					start = r.Intn(end)
				}
				vs = append([]int64(nil), pool[start:end]...)
			}
			micro = append(micro, history.MicroOp{F: history.MicroRead, Key: k, Values: vs})
		}
		in := history.Op{Index: i, Process: i, Type: history.Invoke, F: history.FTxn, Txn: micro}
		out := history.Op{Process: i, Type: history.OK, F: history.FTxn, Txn: micro}
		if !hasRead && infoCount < 2 && r.Bool(0.15) {
			infoCount++
			out.Type, ret = history.Info, history.Infinity
		}
		ops = append(ops, history.Operation{ID: i, Process: i, Call: call, Return: ret, Input: in, Output: out})
	}
	return ops
}

// TestDifferentialOptimizerEquivalence verifies that every WGL optimization mode
// (default fast paths, SearchOnly, disabled eagerRead, disabled kvPruner, and
// both optimizers disabled) agrees with the brute-force oracle across multiple
// models on randomized small histories.
func TestDifferentialOptimizerEquivalence(t *testing.T) {
	modes := []struct {
		name string
		opts Options
	}{
		{"default", Options{Parallelism: 1}},
		{"search_only", Options{Parallelism: 1, SearchOnly: true}},
		{"no_eager_read", Options{Parallelism: 1, disableEagerRead: true}},
		{"no_pruner", Options{Parallelism: 1, disablePruner: true}},
		{"baseline_wgl", Options{Parallelism: 1, disableEagerRead: true, disablePruner: true}},
	}

	r := rng.New(9991)
	var appendSeq int64
	models := []struct {
		name  string
		model model.Model
		iters int
		gen   func() []history.Operation
	}{
		{"kv", model.KV{}, 600, func() []history.Operation { return randomHistory(r, 1+r.Intn(6)) }},
		{"set", model.Set{}, 200, func() []history.Operation { return randomSetHistory(r, 1+r.Intn(6)) }},
		{"counter", model.Counter{}, 200, func() []history.Operation { return randomCounterHistory(r, 1+r.Intn(6)) }},
		{"queue", model.Queue{}, 200, func() []history.Operation { return randomQueueHistory(r, 1+r.Intn(6)) }},
		{"idgen", model.IDGen{}, 200, func() []history.Operation { return randomIDGenHistory(r, 1+r.Intn(6)) }},
		{"append_strict", model.Append{Level: "strict-serializable"}, 200, func() []history.Operation { return randomAppendHistory(r, 1+r.Intn(6), &appendSeq) }},
	}

	for _, mc := range models {
		t.Run(mc.name, func(t *testing.T) {
			counts := map[Verdict]int{}
			for iter := 0; iter < mc.iters; iter++ {
				ops := mc.gen()
				want := bruteForce(mc.model, ops)
				for _, mode := range modes {
					got := CheckOperations(mc.model, ops, mode.opts)
					if (got.Verdict == Linearizable) != want {
						t.Fatalf("iter %d mode %s: got=%s wantLinearizable=%v\n%s", iter, mode.name, got.Verdict, want, dump(ops))
					}
					if mode.name == "default" {
						counts[got.Verdict]++
					}
				}
			}
			if counts[Linearizable] == 0 || counts[Violation] == 0 {
				t.Fatalf("model %s histories lacked both outcomes: %v", mc.name, counts)
			}
		})
	}
}

func TestDeciderSoundIncompleteAndBoundedCompleteSearch(t *testing.T) {
	// 1. Empty sound-incomplete model history proves Linearizable.
	for _, m := range []model.Model{
		model.Append{Level: "serializable"},
		model.Ledger{InitialBalance: 1000},
		model.Settlement{},
	} {
		rep := CheckOperations(m, nil, Options{})
		if rep.Verdict != Linearizable || rep.IsIncomplete() || !rep.NoAnomalyFound() {
			t.Fatalf("%s empty history: got verdict=%s incomplete=%v noAnomaly=%v",
				m.Name(), rep.Verdict, rep.IsIncomplete(), rep.NoAnomalyFound())
		}
	}

	// 2. Clean non-strict Append returns Unknown ("sound incomplete"), not Linearizable.
	nonStrict := []history.Operation{
		{
			ID: 0, Process: 0, Call: 10, Return: 20,
			Input:  history.Op{Index: 0, Process: 0, Type: history.Invoke, F: history.FTxn, Txn: []history.MicroOp{{F: history.MicroAppend, Key: "k0", Value: v(1)}}},
			Output: history.Op{Index: 1, Process: 0, Type: history.OK, F: history.FTxn, Txn: []history.MicroOp{{F: history.MicroAppend, Key: "k0", Value: v(1)}}},
		},
	}
	repSer := CheckOperations(model.Append{Level: "serializable"}, nonStrict, Options{})
	if repSer.Verdict != Unknown || !repSer.IsIncomplete() || !repSer.NoAnomalyFound() {
		t.Fatalf("serializable append: got verdict=%s reason=%q incomplete=%v noAnomaly=%v",
			repSer.Verdict, repSer.Keys[0].Reason, repSer.IsIncomplete(), repSer.NoAnomalyFound())
	}

	// 3. Small clean strict-serializable Append verifies Linearizable via bounded WGL fallback.
	repStrictSmall := CheckOperations(model.Append{Level: "strict-serializable"}, nonStrict, Options{})
	if repStrictSmall.Verdict != Linearizable || repStrictSmall.IsIncomplete() || !repStrictSmall.NoAnomalyFound() {
		t.Fatalf("small strict append: got verdict=%s incomplete=%v noAnomaly=%v",
			repStrictSmall.Verdict, repStrictSmall.IsIncomplete(), repStrictSmall.NoAnomalyFound())
	}

	// 4. Large clean strict-serializable Append (> 16 ops) yields Unknown ("sound incomplete").
	var large []history.Operation
	var cur []int64
	for i := 0; i < 18; i++ {
		val := int64(i + 1)
		cur = append(slices.Clone(cur), val)
		micro := []history.MicroOp{
			{F: history.MicroAppend, Key: "k0", Value: v(val)},
			{F: history.MicroRead, Key: "k0", Values: cur},
		}
		large = append(large, history.Operation{
			ID: i, Process: 0, Call: int64(i * 10), Return: int64(i*10 + 5),
			Input:  history.Op{Index: i * 2, Process: 0, Type: history.Invoke, F: history.FTxn, Txn: micro},
			Output: history.Op{Index: i*2 + 1, Process: 0, Type: history.OK, F: history.FTxn, Txn: micro},
		})
	}
	repLarge := CheckOperations(model.Append{Level: "strict-serializable"}, large, Options{})
	if repLarge.Verdict != Unknown || !repLarge.IsIncomplete() || !repLarge.NoAnomalyFound() {
		t.Fatalf("large strict append: got verdict=%s reason=%q incomplete=%v noAnomaly=%v",
			repLarge.Verdict, repLarge.Keys[0].Reason, repLarge.IsIncomplete(), repLarge.NoAnomalyFound())
	}

	// 5. Budget exhaustion Unknown is distinct from sound-incomplete and does NOT satisfy NoAnomalyFound.
	kvOps := generateConcurrentKVHistory(2026, 40, 8, 4)
	repBudget := CheckOperations(model.KV{}, kvOps, Options{SearchOnly: true, MaxSteps: 1})
	if repBudget.Verdict != Unknown || repBudget.Keys[0].Reason != ReasonSteps || repBudget.IsIncomplete() || repBudget.NoAnomalyFound() {
		t.Fatalf("budget exhaustion: got verdict=%s reason=%q incomplete=%v noAnomaly=%v",
			repBudget.Verdict, repBudget.Keys[0].Reason, repBudget.IsIncomplete(), repBudget.NoAnomalyFound())
	}
}
