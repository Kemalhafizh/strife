package model

import (
	"strings"
	"testing"

	"github.com/kemalhafizh/strife/pkg/history"
)

func appendOp(id, proc int, call, ret int64, status history.Type, micro ...history.MicroOp) history.Operation {
	in := make([]history.MicroOp, len(micro))
	for i, m := range micro {
		in[i] = history.MicroOp{F: m.F, Key: m.Key, Value: m.Value}
	}
	o := history.Operation{ID: id, Process: proc, Call: call, Return: ret,
		Input:  history.Op{Process: proc, Type: history.Invoke, F: history.FTxn, Txn: in},
		Output: history.Op{Process: proc, Type: status, F: history.FTxn}}
	if status == history.OK {
		o.Output.Txn = micro
	}
	return o
}

func app(k string, v int64) history.MicroOp {
	return history.MicroOp{F: history.MicroAppend, Key: k, Value: history.Int(v)}
}

func rd(k string, vs ...int64) history.MicroOp {
	return history.MicroOp{F: history.MicroRead, Key: k, Values: vs}
}

func TestAppendModel(t *testing.T) {
	m := Append{Level: "strict-serializable"}
	good := []history.Operation{
		appendOp(0, 0, 0, 10, history.OK, app("x", 1)),
		appendOp(1, 1, 20, 30, history.OK, rd("x", 1), app("x", 2)),
		appendOp(2, 0, 40, 50, history.OK, rd("x", 1, 2)),
	}
	if bad, _, _, _ := m.Decide(good); bad {
		t.Fatal("serial history flagged")
	}
	// The sequential spec agrees.
	s := m.Init()
	for _, o := range good {
		var ok bool
		if ok, s = m.Step(s, o.Input, o.Output); !ok {
			t.Fatalf("step rejected %s", o.Describe())
		}
	}
	if s.String() != "x=[1 2]" {
		t.Fatalf("state %s", s)
	}

	stale := append(good, appendOp(3, 1, 60, 70, history.OK, rd("x", 1)))
	bad, witness, diag, kinds := m.Decide(stale)
	if !bad || len(witness) == 0 || !strings.Contains(diag, "G-realtime") || kinds[0] != "G-realtime" {
		t.Fatalf("stale read after completion must be a real-time violation: %v %v %q", bad, witness, diag)
	}
	if ok, _ := m.Step(s, stale[3].Input, stale[3].Output); ok {
		t.Fatal("step must reject a stale list")
	}
	// Serializable (not strict) allows ordering the stale reader earlier.
	if bad, _, _, _ := (Append{Level: "serializable"}).Decide(stale); bad {
		t.Fatal("serializability does not forbid this history")
	}
}
