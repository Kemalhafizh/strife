package shrink

import (
	"context"
	"errors"
	"testing"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
)

func v(n int64) *int64 { return &n }

func TestHistoryShrinkKVStaleRead(t *testing.T) {
	// Build a history with noise operations across keys, but one stale read on k0:
	// w1: p0 writes k0 -> 1 (call=10, ret=20)
	// w2: p0 writes k0 -> 2 (call=30, ret=40)
	// r1: p1 reads  k0 -> 1 (call=50, ret=60) [stale read!]
	var ops []history.Op
	timeCursor := int64(0)
	addOp := func(proc int, f, key string, val *int64) {
		timeCursor += 10
		ops = append(ops, history.Op{Process: proc, Type: history.Invoke, F: f, Key: key, Value: val, Time: timeCursor})
		timeCursor += 10
		ops = append(ops, history.Op{Process: proc, Type: history.OK, F: f, Key: key, Value: val, Time: timeCursor})
	}

	// Noise on other keys
	for i := 0; i < 20; i++ {
		addOp(2, history.FWrite, "k1", v(int64(i)))
		addOp(3, history.FRead, "k1", v(int64(i)))
	}

	// The conflicting sequence on k0
	addOp(0, history.FWrite, "k0", v(1))
	addOp(0, history.FWrite, "k0", v(2))
	addOp(1, history.FRead, "k0", v(1)) // stale read

	// More noise
	for i := 20; i < 40; i++ {
		addOp(2, history.FWrite, "k2", v(int64(i)))
		addOp(3, history.FRead, "k2", v(int64(i)))
	}

	res, err := History(context.Background(), model.KV{}, ops, HistoryOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Verdict != checker.Violation {
		t.Fatalf("expected violation, got %s", res.Verdict)
	}
	if res.Key != "k0" {
		t.Errorf("expected violating key k0, got %q", res.Key)
	}
	if res.MinimalOps > 4 {
		t.Errorf("expected at most 4 operations in minimal history, got %d", res.MinimalOps)
	}

	// Verify the minimal history on its own still violates
	chk, err := checker.Check(model.KV{}, res.History, checker.Options{})
	if err != nil {
		t.Fatalf("re-check error: %v", err)
	}
	if chk.Verdict != checker.Violation {
		t.Fatalf("minimal history must violate linearizability, got %s", chk.Verdict)
	}
}

func TestHistoryShrinkQueue(t *testing.T) {
	var ops []history.Op
	tCursor := int64(0)
	addOp := func(proc int, f, key string, val *int64) {
		tCursor += 10
		ops = append(ops, history.Op{Process: proc, Type: history.Invoke, F: f, Key: key, Value: val, Time: tCursor})
		tCursor += 10
		ops = append(ops, history.Op{Process: proc, Type: history.OK, F: f, Key: key, Value: val, Time: tCursor})
	}

	// Noise operations
	for i := 100; i < 110; i++ {
		addOp(1, history.FEnqueue, "q_other", v(int64(i)))
		addOp(2, history.FDequeue, "q_other", v(int64(i)))
	}

	// FIFO order inversion on q:
	// enqueue 10 completes strictly before enqueue 20
	// dequeue returns 20
	addOp(0, history.FEnqueue, "q", v(10))
	addOp(0, history.FEnqueue, "q", v(20))
	addOp(1, history.FDequeue, "q", v(20))

	res, err := History(context.Background(), model.Queue{}, ops, HistoryOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Verdict != checker.Violation {
		t.Fatalf("expected violation, got %s", res.Verdict)
	}
	if res.MinimalOps != 3 {
		t.Errorf("expected exactly 3 operations, got %d", res.MinimalOps)
	}

	chk, _ := checker.Check(model.Queue{}, res.History, checker.Options{})
	if chk.Verdict != checker.Violation {
		t.Fatalf("minimal queue history must violate, got %s", chk.Verdict)
	}
}

func TestHistoryShrinkIDGen(t *testing.T) {
	var ops []history.Op
	tCursor := int64(0)
	addGen := func(id int64) {
		tCursor += 10
		ops = append(ops, history.Op{Process: 0, Type: history.Invoke, F: history.FGenerate, Key: "id", Time: tCursor})
		tCursor += 10
		ops = append(ops, history.Op{Process: 0, Type: history.OK, F: history.FGenerate, Key: "id", Value: v(id), Time: tCursor})
	}

	// 30 unique IDs
	for i := int64(1); i <= 30; i++ {
		addGen(i)
	}
	// Duplicate 99
	addGen(99)
	addGen(99)

	res, err := History(context.Background(), model.IDGen{}, ops, HistoryOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Verdict != checker.Violation {
		t.Fatalf("expected violation, got %s", res.Verdict)
	}
	if res.MinimalOps != 2 {
		t.Errorf("expected 2 operations for duplicate ID, got %d", res.MinimalOps)
	}
}

func TestHistoryShrinkNotViolating(t *testing.T) {
	var ops []history.Op
	tCursor := int64(0)
	tCursor += 10
	ops = append(ops, history.Op{Process: 0, Type: history.Invoke, F: history.FWrite, Key: "x", Value: v(1), Time: tCursor})
	tCursor += 10
	ops = append(ops, history.Op{Process: 0, Type: history.OK, F: history.FWrite, Key: "x", Value: v(1), Time: tCursor})

	tCursor += 10
	ops = append(ops, history.Op{Process: 1, Type: history.Invoke, F: history.FRead, Key: "x", Time: tCursor})
	tCursor += 10
	ops = append(ops, history.Op{Process: 1, Type: history.OK, F: history.FRead, Key: "x", Value: v(1), Time: tCursor})

	_, err := History(context.Background(), model.KV{}, ops, HistoryOptions{})
	if !errors.Is(err, ErrNotViolating) {
		t.Fatalf("expected ErrNotViolating, got %v", err)
	}
}
