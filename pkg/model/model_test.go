package model

import (
	"reflect"
	"testing"

	"github.com/kemalhafizh/strife/pkg/history"
)

var v = history.Int

func TestByNameAndNames(t *testing.T) {
	expectedNames := []string{"kv", "set", "queue", "counter", "idgen", "txn", "append"}
	names := Names()
	if !reflect.DeepEqual(names, expectedNames) {
		t.Fatalf("expected names %v, got %v", expectedNames, names)
	}

	for _, name := range expectedNames {
		m, err := ByName(name)
		if err != nil {
			t.Fatalf("ByName(%q) failed: %v", name, err)
		}
		if m.Name() != name {
			t.Fatalf("m.Name() = %q, want %q", m.Name(), name)
		}
	}

	// Aliases
	aliases := map[string]string{
		"register":      "kv",
		"cas-register":  "kv",
		"fifo":          "queue",
		"fifo-queue":    "queue",
		"uid":           "idgen",
		"unique-id":     "idgen",
		"transaction":   "txn",
		"transactional": "txn",
	}
	for alias, canonical := range aliases {
		m, err := ByName(alias)
		if err != nil || m.Name() != canonical {
			t.Fatalf("alias %q failed: %v", alias, err)
		}
	}

	if _, err := ByName("unknown"); err == nil {
		t.Fatal("expected error for unknown model")
	}
}

func TestQueueModel(t *testing.T) {
	q := Queue{}
	st := q.Init()

	// Enqueue 1, 2
	ok, st := q.Step(st, history.Op{F: "enqueue", Value: v(1)}, history.Op{Type: history.OK})
	if !ok {
		t.Fatal("enqueue 1 failed")
	}
	ok, st = q.Step(st, history.Op{F: "enqueue", Value: v(2)}, history.Op{Type: history.OK})
	if !ok {
		t.Fatal("enqueue 2 failed")
	}

	// Dequeue 1 (head)
	ok, st = q.Step(st, history.Op{F: "dequeue"}, history.Op{Type: history.OK, Value: v(1)})
	if !ok {
		t.Fatal("dequeue 1 failed")
	}

	// Dequeue 2 (next head)
	ok, st = q.Step(st, history.Op{F: "dequeue"}, history.Op{Type: history.OK, Value: v(2)})
	if !ok {
		t.Fatal("dequeue 2 failed")
	}

	// Queue is now empty. Dequeue on empty queue returning nil is valid.
	ok, st = q.Step(st, history.Op{F: "dequeue"}, history.Op{Type: history.OK, Value: nil})
	if !ok {
		t.Fatal("dequeue on empty queue should succeed when observing nil")
	}

	// Dequeue on empty queue returning a value is invalid.
	ok, _ = q.Step(st, history.Op{F: "dequeue"}, history.Op{Type: history.OK, Value: v(99)})
	if ok {
		t.Fatal("dequeue on empty queue should fail when observing non-nil value")
	}

	// State equality and hashing
	stA := q.Init().(queueState).enqueue(1).enqueue(2)
	stB := q.Init().(queueState).enqueue(1).enqueue(2)
	stC := q.Init().(queueState).enqueue(2).enqueue(1)
	if !stA.Equal(stB) || stA.Hash() != stB.Hash() {
		t.Fatal("stA should equal stB")
	}
	if stA.Equal(stC) || stA.Hash() == stC.Hash() {
		t.Fatal("stA should not equal stC")
	}
}

func TestQueueDiagnoser(t *testing.T) {
	q := Queue{}

	t.Run("PhantomNeverEnqueued", func(t *testing.T) {
		ops := []history.Operation{
			{ID: 0, Call: 10, Return: 20, Input: history.Op{Process: 0, F: "dequeue"}, Output: history.Op{Type: history.OK, Value: v(42)}},
		}
		witness, diag, ok := q.Diagnose(ops)
		if !ok || len(witness) != 1 || witness[0] != 0 {
			t.Fatalf("expected phantom diagnosis: ok=%v, witness=%v", ok, witness)
		}
		if diag == "" {
			t.Fatal("empty diagnosis")
		}
	})

	t.Run("FIFOOrderInversion", func(t *testing.T) {
		// Enqueue 1 (0..10), Enqueue 2 (15..20), Dequeue 2 (25..30)
		ops := []history.Operation{
			{ID: 0, Call: 0, Return: 10, Input: history.Op{Process: 0, F: "enqueue", Value: v(1)}, Output: history.Op{Type: history.OK}},
			{ID: 1, Call: 15, Return: 20, Input: history.Op{Process: 1, F: "enqueue", Value: v(2)}, Output: history.Op{Type: history.OK}},
			{ID: 2, Call: 25, Return: 30, Input: history.Op{Process: 2, F: "dequeue"}, Output: history.Op{Type: history.OK, Value: v(2)}},
		}
		witness, diag, ok := q.Diagnose(ops)
		if !ok {
			t.Fatal("expected FIFO order inversion diagnosis")
		}
		if len(witness) < 3 {
			t.Fatalf("expected at least 3 witness operations, got %d", len(witness))
		}
		if diag == "" {
			t.Fatal("empty diagnosis")
		}
	})

	t.Run("PrematureEmptyDequeue", func(t *testing.T) {
		// Enqueue 1 (0..10), Dequeue empty (15..20)
		ops := []history.Operation{
			{ID: 0, Call: 0, Return: 10, Input: history.Op{Process: 0, F: "enqueue", Value: v(1)}, Output: history.Op{Type: history.OK}},
			{ID: 1, Call: 15, Return: 20, Input: history.Op{Process: 1, F: "dequeue"}, Output: history.Op{Type: history.OK, Value: nil}},
		}
		witness, diag, ok := q.Diagnose(ops)
		if !ok || len(witness) != 2 {
			t.Fatalf("expected premature empty diagnosis: ok=%v, witness=%v", ok, witness)
		}
		if diag == "" {
			t.Fatal("empty diagnosis")
		}
	})
}

func TestCounterModel(t *testing.T) {
	c := Counter{}
	st := c.Init()

	// Default increment (+1)
	ok, st := c.Step(st, history.Op{F: "inc"}, history.Op{Type: history.OK})
	if !ok {
		t.Fatal("inc failed")
	}

	// Add +5
	ok, st = c.Step(st, history.Op{F: "add", Value: v(5)}, history.Op{Type: history.OK})
	if !ok {
		t.Fatal("add 5 failed")
	}

	// Read 6
	ok, st = c.Step(st, history.Op{F: "read"}, history.Op{Type: history.OK, Value: v(6)})
	if !ok {
		t.Fatal("read 6 should succeed")
	}

	// Read wrong value
	ok, _ = c.Step(st, history.Op{F: "read"}, history.Op{Type: history.OK, Value: v(5)})
	if ok {
		t.Fatal("read 5 should fail")
	}

	// Check ReadOnly
	if !c.ReadOnly(history.Op{F: "read"}) || !c.ReadOnly(history.Op{F: "get"}) {
		t.Fatal("read and get should be read-only")
	}
	if c.ReadOnly(history.Op{F: "add"}) || c.ReadOnly(history.Op{F: "inc"}) {
		t.Fatal("add and inc should not be read-only")
	}
}

func TestCounterDiagnoser(t *testing.T) {
	c := Counter{}

	t.Run("NegativeValue", func(t *testing.T) {
		ops := []history.Operation{
			{ID: 0, Call: 10, Return: 20, Input: history.Op{Process: 0, F: "read"}, Output: history.Op{Type: history.OK, Value: v(-1)}},
		}
		witness, diag, ok := c.Diagnose(ops)
		if !ok || len(witness) != 1 {
			t.Fatalf("expected negative value diagnosis: ok=%v", ok)
		}
		if diag == "" {
			t.Fatal("empty diagnosis")
		}
	})

	t.Run("StaleCounterRead", func(t *testing.T) {
		// Add 5 (0..10), Add 5 (12..18), Read 5 (25..30)
		ops := []history.Operation{
			{ID: 0, Call: 0, Return: 10, Input: history.Op{Process: 0, F: "add", Value: v(5)}, Output: history.Op{Type: history.OK}},
			{ID: 1, Call: 12, Return: 18, Input: history.Op{Process: 1, F: "add", Value: v(5)}, Output: history.Op{Type: history.OK}},
			{ID: 2, Call: 25, Return: 30, Input: history.Op{Process: 2, F: "read"}, Output: history.Op{Type: history.OK, Value: v(5)}},
		}
		witness, diag, ok := c.Diagnose(ops)
		if !ok {
			t.Fatal("expected stale counter read diagnosis")
		}
		if len(witness) < 2 {
			t.Fatalf("expected at least 2 witness operations, got %d", len(witness))
		}
		if diag == "" {
			t.Fatal("empty diagnosis")
		}
	})

	t.Run("OverstatedCounterRead", func(t *testing.T) {
		// Add 5 (0..10), Read 100 (15..20)
		ops := []history.Operation{
			{ID: 0, Call: 0, Return: 10, Input: history.Op{Process: 0, F: "add", Value: v(5)}, Output: history.Op{Type: history.OK}},
			{ID: 1, Call: 15, Return: 20, Input: history.Op{Process: 1, F: "read"}, Output: history.Op{Type: history.OK, Value: v(100)}},
		}
		witness, diag, ok := c.Diagnose(ops)
		if !ok || len(witness) != 1 || witness[0] != 1 {
			t.Fatalf("expected overstated read diagnosis: ok=%v, witness=%v", ok, witness)
		}
		if diag == "" {
			t.Fatal("empty diagnosis")
		}
	})
}

func TestIDGenModelAndDiagnoser(t *testing.T) {
	idGen := IDGen{}
	st := idGen.Init()

	// Generate 100, 200
	ok, st := idGen.Step(st, history.Op{F: "generate"}, history.Op{Type: history.OK, Value: v(100)})
	if !ok {
		t.Fatal("generate 100 failed")
	}
	ok, st = idGen.Step(st, history.Op{F: "generate"}, history.Op{Type: history.OK, Value: v(200)})
	if !ok {
		t.Fatal("generate 200 failed")
	}

	// Duplicate 100
	ok, _ = idGen.Step(st, history.Op{F: "generate"}, history.Op{Type: history.OK, Value: v(100)})
	if ok {
		t.Fatal("duplicate 100 should fail")
	}

	// Diagnoser finds duplicate
	ops := []history.Operation{
		{ID: 0, Call: 0, Return: 10, Input: history.Op{Process: 0, F: "generate"}, Output: history.Op{Type: history.OK, Value: v(100)}},
		{ID: 1, Call: 15, Return: 25, Input: history.Op{Process: 1, F: "generate"}, Output: history.Op{Type: history.OK, Value: v(100)}},
	}
	witness, diag, ok := idGen.Diagnose(ops)
	if !ok || len(witness) != 2 {
		t.Fatalf("expected duplicate ID diagnosis: ok=%v, witness=%v", ok, witness)
	}
	if diag == "" {
		t.Fatal("empty diagnosis")
	}
}

func TestTxnModelAndDiagnoser(t *testing.T) {
	tm := TxnModel{Level: "serializable"}
	st := tm.Init()

	// Step atomic multi-key write
	inWrite := history.Op{
		F: history.FTxn,
		Txn: []history.MicroOp{
			{F: "w", Key: "x", Value: v(10)},
			{F: "w", Key: "y", Value: v(20)},
		},
	}
	ok, st := tm.Step(st, inWrite, history.Op{Type: history.OK})
	if !ok {
		t.Fatal("Step multi-key write failed")
	}

	// Step multi-key read matching state
	inRead := history.Op{
		F: history.FTxn,
		Txn: []history.MicroOp{
			{F: "r", Key: "x", Value: v(10)},
			{F: "r", Key: "y", Value: v(20)},
		},
	}
	ok, _ = tm.Step(st, inRead, history.Op{Type: history.OK})
	if !ok {
		t.Fatal("Step multi-key read should succeed")
	}

	// Step multi-key read with mismatch
	inMismatch := history.Op{
		F: history.FTxn,
		Txn: []history.MicroOp{
			{F: "r", Key: "x", Value: v(999)},
		},
	}
	ok, _ = tm.Step(st, inMismatch, history.Op{Type: history.OK})
	if ok {
		t.Fatal("Step multi-key read mismatch should fail")
	}

	// Diagnoser finds G1c cycle
	ops := []history.Operation{
		{
			ID:     0,
			Call:   0,
			Return: 10,
			Input: history.Op{
				Process: 0,
				F:       history.FTxn,
				Txn: []history.MicroOp{
					{F: "w", Key: "x", Value: v(1)},
					{F: "r", Key: "y", Value: v(2)},
				},
			},
			Output: history.Op{Type: history.OK},
		},
		{
			ID:     1,
			Call:   0,
			Return: 10,
			Input: history.Op{
				Process: 1,
				F:       history.FTxn,
				Txn: []history.MicroOp{
					{F: "w", Key: "y", Value: v(2)},
					{F: "r", Key: "x", Value: v(1)},
				},
			},
			Output: history.Op{Type: history.OK},
		},
	}

	witness, diag, ok := tm.Diagnose(ops)
	if !ok || len(witness) != 2 {
		t.Fatalf("expected G1c diagnosis from TxnModel: ok=%v, witness=%v", ok, witness)
	}
	if diag == "" {
		t.Fatal("empty diagnosis")
	}
}

func TestWithIsolation(t *testing.T) {
	if m := WithIsolation(TxnModel{}, "snapshot-isolation"); m.(TxnModel).Level != "snapshot-isolation" {
		t.Fatalf("txn: %+v", m)
	}
	if m := WithIsolation(Append{}, "serializable"); m.(Append).Level != "serializable" {
		t.Fatalf("append: %+v", m)
	}
	if m := WithIsolation(KV{}, "serializable"); m != (KV{}) {
		t.Fatalf("a non-transactional model must come back unchanged: %+v", m)
	}
}
