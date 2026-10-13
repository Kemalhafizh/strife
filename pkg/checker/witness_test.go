package checker

import (
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/internal/rng"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
)

// TestDiagnosisIsSound: whenever the KV model claims a provable anomaly, the
// history must really be non-linearizable (exhaustively verified).
func TestDiagnosisIsSound(t *testing.T) {
	r := rng.New(77)
	claims := 0
	for iter := 0; iter < 20000; iter++ {
		ops := randomHistory(r, 1+r.Intn(6))
		idx, diag, ok := model.KV{}.Diagnose(ops)
		if !ok {
			continue
		}
		claims++
		if bruteForce(model.KV{}, ops) {
			t.Fatalf("iter %d: diagnosed %q (witness %v) but the history is linearizable:\n%s", iter, diag, idx, dump(ops))
		}
		if len(idx) == 0 || len(idx) > 3 || diag == "" {
			t.Fatalf("bad diagnosis %v %q", idx, diag)
		}
	}
	if claims < 500 {
		t.Fatalf("diagnosis almost never fires (%d claims): test is not exercising it", claims)
	}
}

// TestGenericWitnessIsSound: the fallback only removes reads, so whatever it
// returns must still be non-linearizable, and the original must be too.
func TestGenericWitnessIsSound(t *testing.T) {
	r := rng.New(78)
	found := 0
	for iter := 0; iter < 3000; iter++ {
		ops := randomHistory(r, 2+r.Intn(5))
		if bruteForce(model.KV{}, ops) {
			continue
		}
		// Use a model without a Diagnoser to force the generic path.
		w, diag := Witness(noDiag{}, ops, WitnessOptions{})
		if w == nil {
			continue
		}
		found++
		if bruteForce(model.KV{}, w) {
			t.Fatalf("iter %d: generic witness is linearizable:\n%s\nfrom\n%s", iter, dump(w), dump(ops))
		}
		if !strings.Contains(diag, "cannot be ordered") {
			t.Fatalf("diag %q", diag)
		}
		// Only reads may be dropped: every other operation must survive.
		writes := 0
		for _, op := range ops {
			if op.Input.F != history.FRead {
				writes++
			}
		}
		nonReads := 0
		for _, op := range w {
			if op.Input.F != history.FRead {
				nonReads++
			}
		}
		if nonReads != writes {
			t.Fatalf("generic witness dropped a write (%d of %d kept)", nonReads, writes)
		}
	}
	if found < 100 {
		t.Fatalf("generic path barely exercised: %d", found)
	}
}

// noDiag is the KV model without its Diagnoser: the method below has a
// different signature, so it hides KV.Diagnose instead of implementing it.
type noDiag struct{ model.KV }

func (noDiag) Diagnose() {}

func TestAddWitnessesOnStaleRead(t *testing.T) {
	b := &builder{}
	b.add(inv(0, "write", "x", v(1))).add(done(0, history.OK, "write", "x", v(1)))
	b.add(inv(1, "read", "x", nil)).add(done(1, history.OK, "read", "x", v(1)))
	b.add(inv(0, "write", "x", v(2))).add(done(0, history.OK, "write", "x", v(2)))
	b.add(inv(1, "read", "x", nil)).add(done(1, history.OK, "read", "x", v(2)))
	b.add(inv(2, "read", "x", nil)).add(done(2, history.OK, "read", "x", v(1)))
	rep := mustCheck(t, b.ops)
	if err := AddWitnesses(model.KV{}, b.ops, &rep, WitnessOptions{}); err != nil {
		t.Fatal(err)
	}
	ce := rep.Keys[0].Counterexample
	if len(ce.Witness) != 3 || !strings.HasPrefix(ce.Diagnosis, "Stale read: p2 read x → 1") {
		t.Fatalf("witness %+v diagnosis %q", ce.Witness, ce.Diagnosis)
	}
	if ce.Summary() != ce.Diagnosis {
		t.Fatal("summary should prefer the diagnosis")
	}
}

func TestPhantomValue(t *testing.T) {
	b := &builder{}
	b.add(inv(0, "write", "x", v(1))).add(done(0, history.OK, "write", "x", v(1)))
	b.add(inv(1, "read", "x", nil)).add(done(1, history.OK, "read", "x", v(7)))
	rep := mustCheck(t, b.ops)
	_ = AddWitnesses(model.KV{}, b.ops, &rep, WitnessOptions{})
	if d := rep.Keys[0].Counterexample.Diagnosis; !strings.HasPrefix(d, "Phantom value") {
		t.Fatalf("got %q", d)
	}
}

func TestDdmin(t *testing.T) {
	items := make([]int, 100)
	for i := range items {
		items[i] = i
	}
	calls := 0
	got := ddmin(items, func(s []int) bool {
		calls++
		has := map[int]bool{}
		for _, x := range s {
			has[x] = true
		}
		return has[13] && has[71] && has[72]
	})
	if len(got) != 3 || got[0] != 13 || got[1] != 71 || got[2] != 72 {
		t.Fatalf("got %v", got)
	}
	if calls > 200 {
		t.Fatalf("ddmin too slow: %d calls", calls)
	}
}

func TestTimeout(t *testing.T) {
	// A pathological history: many concurrent indeterminate writes and reads
	// that cannot all be satisfied make the search explode.
	var ops []history.Operation
	for i := 0; i < 40; i++ {
		in := history.Op{Index: i, Process: i, Type: history.Invoke, F: history.FWrite, Key: "x", Value: v(int64(i % 7))}
		ops = append(ops, history.Operation{ID: i, Process: i, Call: 0, Return: history.Infinity, Input: in, Output: history.Op{Type: history.Info}})
	}
	for i := 40; i < 60; i++ {
		in := history.Op{Index: i, Process: i, Type: history.Invoke, F: history.FRead, Key: "x"}
		ops = append(ops, history.Operation{ID: i, Process: i, Call: int64(i), Return: int64(i) + 1, Input: in, Output: history.Op{Type: history.OK, Value: v(int64(i % 9))}})
	}
	// The diagnosis proves this instantly (reads of values nobody wrote)...
	if rep := CheckOperations(model.KV{}, ops, Options{MaxSteps: 1000}); rep.Verdict != Violation || rep.Keys[0].Steps != 0 {
		t.Fatalf("fast path should prove it without searching: %+v", rep.Keys[0])
	}
	// ...so force the search to exercise its budgets.
	rep := CheckOperations(model.KV{}, ops, Options{SearchOnly: true, Timeout: 50 * time.Millisecond})
	if rep.Verdict == Unknown && rep.Keys[0].Reason != ReasonTime {
		t.Fatalf("unknown for the wrong reason: %+v", rep.Keys[0])
	}
	rep = CheckOperations(model.KV{}, ops, Options{SearchOnly: true, MaxSteps: 1000})
	if rep.Verdict != Unknown || rep.Keys[0].Reason != ReasonSteps {
		t.Fatalf("want step budget exhaustion, got %+v", rep.Keys[0])
	}
}
