package model_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
)

func i64(v int64) *int64 { return &v }

func makeTxnOp(id, proc int, call, ret int64, status history.Type, key string, micro []history.MicroOp) history.Operation {
	in := history.Op{
		Index:   id * 2,
		Process: proc,
		Type:    history.Invoke,
		F:       history.FTxn,
		Key:     key,
		Txn:     micro,
		Time:    call,
	}
	out := history.Op{
		Index:   id*2 + 1,
		Process: proc,
		Type:    status,
		F:       history.FTxn,
		Key:     key,
		Txn:     micro,
		Time:    ret,
	}
	return history.Operation{
		ID:      id,
		Process: proc,
		Call:    call,
		Return:  ret,
		Input:   in,
		Output:  out,
	}
}

func TestLedgerByName(t *testing.T) {
	for _, alias := range []string{"ledger", "double-entry", "bank-ledger"} {
		m, err := model.ByName(alias)
		if err != nil {
			t.Fatalf("ByName(%q): %v", alias, err)
		}
		if m.Name() != "ledger" {
			t.Fatalf("expected name ledger, got %q", m.Name())
		}
	}
}

func TestLedgerValidDoubleEntryHistory(t *testing.T) {
	// 3 accounts starting at 1000 each => total supply M_0 = 3000.
	ops := []history.Operation{
		// T0: transfer 250 from acct:0 to acct:1 (1000->750, 1000->1250)
		makeTxnOp(0, 0, 10, 20, history.OK, "tx:1", []history.MicroOp{
			{F: "r", Key: "acct:0", Value: i64(1000)},
			{F: "r", Key: "acct:1", Value: i64(1000)},
			{F: "w", Key: "acct:0", Value: i64(750)},
			{F: "w", Key: "acct:1", Value: i64(1250)},
		}),
		// T1: transfer 400 from acct:1 to acct:2 (1250->850, 1000->1400)
		makeTxnOp(1, 1, 25, 35, history.OK, "tx:2", []history.MicroOp{
			{F: "r", Key: "acct:1", Value: i64(1250)},
			{F: "r", Key: "acct:2", Value: i64(1000)},
			{F: "w", Key: "acct:1", Value: i64(850)},
			{F: "w", Key: "acct:2", Value: i64(1400)},
		}),
		// T2: audit all 3 accounts => 750 + 850 + 1400 = 3000
		makeTxnOp(2, 2, 40, 50, history.OK, "audit", []history.MicroOp{
			{F: "r", Key: "acct:0", Value: i64(750)},
			{F: "r", Key: "acct:1", Value: i64(850)},
			{F: "r", Key: "acct:2", Value: i64(1400)},
		}),
	}

	m := model.Ledger{InitialBalance: 1000}
	bad, witness, diag, kinds := m.Decide(ops)
	if bad {
		t.Fatalf("expected valid ledger history, got violation: %s (witness=%v, kinds=%v)", diag, witness, kinds)
	}

	rep := checker.CheckOperations(m, ops, checker.Options{SearchOnly: true})
	if rep.Verdict != checker.Linearizable {
		t.Fatalf("expected SearchOnly linearizable, got %s", rep.Verdict)
	}
}

func TestLedgerUnbalancedJournalViolation(t *testing.T) {
	// Transfer debits acct:0 by 300 (1000->700) but credits acct:1 by only 250 (1000->1250).
	ops := []history.Operation{
		makeTxnOp(0, 0, 10, 20, history.OK, "tx:unbal", []history.MicroOp{
			{F: "r", Key: "acct:0", Value: i64(1000)},
			{F: "r", Key: "acct:1", Value: i64(1000)},
			{F: "w", Key: "acct:0", Value: i64(700)},
			{F: "w", Key: "acct:1", Value: i64(1250)},
		}),
	}

	m := model.Ledger{InitialBalance: 1000}
	bad, witness, diag, kinds := m.Decide(ops)
	if !bad {
		t.Fatal("expected LEDGER-UNBALANCED violation, got pass")
	}
	if !slices.Contains(kinds, model.AnomalyLedgerUnbalanced) {
		t.Fatalf("expected %s in kinds %v (diag=%s)", model.AnomalyLedgerUnbalanced, kinds, diag)
	}
	if len(witness) == 0 || witness[0] != 0 {
		t.Fatalf("unexpected witness: %v", witness)
	}

	rep := checker.CheckOperations(m, ops, checker.Options{SearchOnly: true})
	if rep.Verdict != checker.Violation {
		t.Fatalf("expected SearchOnly violation, got %s", rep.Verdict)
	}
}

func TestLedgerOverdraftWriteSkewViolation(t *testing.T) {
	// Classic G2-item Overdraft Write Skew:
	// Two concurrent transfers both read acct:0 = 1000 and debit 600 each
	// (600 + 600 = 1200 > 1000).
	ops := []history.Operation{
		makeTxnOp(0, 0, 10, 50, history.OK, "tx:skew-1", []history.MicroOp{
			{F: "r", Key: "acct:0", Value: i64(1000)},
			{F: "r", Key: "acct:1", Value: i64(1000)},
			{F: "w", Key: "acct:0", Value: i64(400)},
			{F: "w", Key: "acct:1", Value: i64(1600)},
		}),
		makeTxnOp(1, 1, 15, 55, history.OK, "tx:skew-2", []history.MicroOp{
			{F: "r", Key: "acct:0", Value: i64(1000)},
			{F: "r", Key: "acct:2", Value: i64(1000)},
			{F: "w", Key: "acct:0", Value: i64(400)},
			{F: "w", Key: "acct:2", Value: i64(1600)},
		}),
	}

	m := model.Ledger{InitialBalance: 1000}
	bad, witness, diag, kinds := m.Decide(ops)
	if !bad {
		t.Fatal("expected LEDGER-OVERDRAFT violation, got pass")
	}
	if !slices.Contains(kinds, model.AnomalyLedgerOverdraft) {
		t.Fatalf("expected %s in kinds %v (diag=%s)", model.AnomalyLedgerOverdraft, kinds, diag)
	}
	if !slices.Contains(kinds, model.AnomalyLedgerConservation) {
		t.Fatalf("expected %s in kinds %v (diag=%s)", model.AnomalyLedgerConservation, kinds, diag)
	}
	if len(witness) != 2 {
		t.Fatalf("expected 2-op witness, got %v", witness)
	}
}

func TestLedgerConservationAuditFracturedRead(t *testing.T) {
	// Audit reads acct:0 after transfer (700) and acct:1 before transfer (1000),
	// observing total supply 1700 != 2000.
	ops := []history.Operation{
		makeTxnOp(0, 0, 10, 40, history.OK, "tx:1", []history.MicroOp{
			{F: "r", Key: "acct:0", Value: i64(1000)},
			{F: "r", Key: "acct:1", Value: i64(1000)},
			{F: "w", Key: "acct:0", Value: i64(700)},
			{F: "w", Key: "acct:1", Value: i64(1300)},
		}),
		makeTxnOp(1, 1, 20, 50, history.OK, "audit", []history.MicroOp{
			{F: "r", Key: "acct:0", Value: i64(700)},
			{F: "r", Key: "acct:1", Value: i64(1000)},
		}),
	}

	m := model.Ledger{InitialBalance: 1000}
	bad, _, diag, kinds := m.Decide(ops)
	if !bad {
		t.Fatal("expected LEDGER-CONSERVATION violation, got pass")
	}
	if !slices.Contains(kinds, model.AnomalyLedgerConservation) {
		t.Fatalf("expected %s in kinds %v (diag=%s)", model.AnomalyLedgerConservation, kinds, diag)
	}
	if !strings.Contains(diag, "discrepancy -300") {
		t.Fatalf("expected discrepancy -300 in diagnosis, got: %s", diag)
	}
}

func TestLedgerDoubleSpendAcrossTimeoutRetry(t *testing.T) {
	// Client invokes transfer "tx:pay-99" of 200 from acct:0 to acct:1, which
	// times out (Info), and then retries "tx:pay-99" under a new process,
	// reading acct:0 = 800 and deducting 200 a second time to 600.
	ops := []history.Operation{
		makeTxnOp(0, 0, 10, history.Infinity, history.Info, "tx:pay-99", []history.MicroOp{
			{F: "r", Key: "acct:0", Value: i64(1000)},
			{F: "r", Key: "acct:1", Value: i64(1000)},
			{F: "w", Key: "acct:0", Value: i64(800)},
			{F: "w", Key: "acct:1", Value: i64(1200)},
		}),
		makeTxnOp(1, 10, 30, 50, history.OK, "tx:pay-99", []history.MicroOp{
			{F: "r", Key: "acct:0", Value: i64(800)},
			{F: "r", Key: "acct:1", Value: i64(1200)},
			{F: "w", Key: "acct:0", Value: i64(600)},
			{F: "w", Key: "acct:1", Value: i64(1400)},
		}),
	}

	m := model.Ledger{InitialBalance: 1000}
	bad, witness, diag, kinds := m.Decide(ops)
	if !bad {
		t.Fatal("expected LEDGER-DOUBLE-SPEND violation, got pass")
	}
	if !slices.Contains(kinds, model.AnomalyLedgerDoubleSpend) {
		t.Fatalf("expected %s in kinds %v (diag=%s)", model.AnomalyLedgerDoubleSpend, kinds, diag)
	}
	if len(witness) != 2 {
		t.Fatalf("expected 2-op witness [0, 1], got %v", witness)
	}
}
