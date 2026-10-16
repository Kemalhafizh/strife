package checker

import (
	"fmt"
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
