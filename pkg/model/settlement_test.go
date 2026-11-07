package model

import (
	"slices"
	"strings"
	"testing"

	"github.com/kemalhafizh/strife/pkg/history"
)

func TestSettlementModelValidHistory(t *testing.T) {
	ops := []history.Operation{
		{
			Call: 10, Return: 20,
			Input:  history.Op{Index: 0, Process: 0, Type: history.Invoke, F: history.FTxn, Key: "settlement", Value: history.Int(101)},
			Output: history.Op{Index: 1, Process: 0, Type: history.OK, F: history.FTxn, Key: "settlement", Txn: []history.MicroOp{{F: history.MicroAppend, Key: "ledger", Value: history.Int(101)}, {F: history.MicroAppend, Key: "broker", Value: history.Int(101)}}},
		},
		{
			Call: 30, Return: 40,
			Input:  history.Op{Index: 2, Process: 1, Type: history.Invoke, F: history.FTxn, Key: "settlement"},
			Output: history.Op{Index: 3, Process: 1, Type: history.OK, F: history.FTxn, Key: "settlement", Txn: []history.MicroOp{{F: history.MicroRead, Key: "ledger", Values: []int64{101}}, {F: history.MicroRead, Key: "broker", Values: []int64{101}}}},
		},
	}

	m := Settlement{}
	violation, witness, diag, kinds := m.Decide(ops)
	if violation {
		t.Fatalf("expected valid settlement history, got violation: %s (kinds=%v, witness=%v)", diag, kinds, witness)
	}
}

func TestSettlementDetectsAtomicityViolation(t *testing.T) {
	ops := []history.Operation{
		{
			Call: 10, Return: 20,
			Input:  history.Op{Index: 0, Process: 0, Type: history.Invoke, F: history.FTxn, Key: "settlement", Value: history.Int(101)},
			Output: history.Op{Index: 1, Process: 0, Type: history.OK, F: history.FTxn, Key: "settlement", Txn: []history.MicroOp{{F: history.MicroAppend, Key: "ledger", Value: history.Int(101)}, {F: history.MicroAppend, Key: "broker", Value: history.Int(101)}}},
		},
		// Dual-write crash: ledger has 101, broker is empty!
		{
			Call: 30, Return: 40,
			Input:  history.Op{Index: 2, Process: 1, Type: history.Invoke, F: history.FTxn, Key: "settlement"},
			Output: history.Op{Index: 3, Process: 1, Type: history.OK, F: history.FTxn, Key: "settlement", Txn: []history.MicroOp{{F: history.MicroRead, Key: "ledger", Values: []int64{101}}, {F: history.MicroRead, Key: "broker", Values: nil}}},
		},
	}

	m := Settlement{}
	violation, _, diag, kinds := m.Decide(ops)
	if !violation || !slices.Contains(kinds, AnomalySettlementAtomicity) {
		t.Fatalf("expected %s violation, got violation=%v kinds=%v diag=%q", AnomalySettlementAtomicity, violation, kinds, diag)
	}
}

func TestSettlementDetectsOrphanCreditAndDuplicate(t *testing.T) {
	ops := []history.Operation{
		// Broker has 999 which was never committed to ledger, plus duplicate 500
		{
			Call: 10, Return: 20,
			Input:  history.Op{Index: 0, Process: 0, Type: history.Invoke, F: history.FTxn, Key: "settlement"},
			Output: history.Op{Index: 1, Process: 0, Type: history.OK, F: history.FTxn, Key: "settlement", Txn: []history.MicroOp{{F: history.MicroRead, Key: "ledger", Values: []int64{500}}, {F: history.MicroRead, Key: "broker", Values: []int64{500, 500, 999}}}},
		},
	}

	m := Settlement{}
	violation, _, diag, kinds := m.Decide(ops)
	if !violation {
		t.Fatalf("expected violation, got none")
	}
	if !slices.Contains(kinds, AnomalySettlementOrphan) || !slices.Contains(kinds, AnomalySettlementIdempotency) {
		t.Fatalf("expected both %s and %s, got kinds=%v (diag=%s)",
			AnomalySettlementOrphan, AnomalySettlementIdempotency, kinds, strings.TrimSpace(diag))
	}
}
