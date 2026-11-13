package history

import (
	"slices"
	"strings"
	"testing"
)

func TestReadEDNRegisterAndCAS(t *testing.T) {
	edn := `
; Jepsen history.edn sample
{:type :invoke, :f :write, :value [:k0 10], :time 1000, :process 0, :index 0}
{:type :ok,     :f :write, :value [:k0 10], :time 2000, :process 0, :index 1}
{:type :info,   :f :start-partition, :value :majority, :time 2500, :process :nemesis, :index 2}
{:type :invoke, :f :cas,   :value [:k0 [10 20]], :time 3000, :process 1, :index 3}
{:type :ok,     :f :cas,   :value [:k0 [10 20]], :time 4000, :process 1, :index 4}
{:type :invoke, :f :read,  :value [:k0 nil], :time 5000, :process 0, :index 5}
{:type :ok,     :f :read,  :value [:k0 20],  :time 6000, :process 0, :index 6}
`
	ops, err := ReadJSONL(strings.NewReader(edn))
	if err != nil {
		t.Fatalf("ReadJSONL (EDN auto-detect): %v", err)
	}
	if len(ops) != 6 {
		t.Fatalf("expected 6 client ops (nemesis filtered), got %d", len(ops))
	}
	paired, err := Pair(ops)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if len(paired) != 3 {
		t.Fatalf("expected 3 paired operations, got %d", len(paired))
	}
	if paired[1].Input.F != FCas || paired[1].Input.Key != "k0" || *paired[1].Input.Old != 10 || *paired[1].Input.Value != 20 {
		t.Fatalf("unexpected CAS op: %+v", paired[1].Input)
	}
	if paired[2].Output.F != FRead || paired[2].Output.Key != "k0" || *paired[2].Output.Value != 20 {
		t.Fatalf("unexpected Read completion: %+v", paired[2].Output)
	}
}

func TestReadEDNElleAppendTransactions(t *testing.T) {
	edn := `[
  {:type :invoke, :f :txn, :value [[:append 0 1] [:r 0 nil]], :time 100, :process 0}
  {:type :ok,     :f :txn, :value [[:append 0 1] [:r 0 [1]]], :time 200, :process 0}
]`
	ops, err := ReadJSONL(strings.NewReader(edn))
	if err != nil {
		t.Fatalf("ReadJSONL (EDN vector): %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected 2 ops, got %d", len(ops))
	}
	if len(ops[1].Txn) != 2 {
		t.Fatalf("expected 2 micro-ops in txn, got %d", len(ops[1].Txn))
	}
	if ops[1].Txn[0].F != MicroAppend || ops[1].Txn[0].Key != "0" || *ops[1].Txn[0].Value != 1 {
		t.Fatalf("unexpected micro-op 0: %+v", ops[1].Txn[0])
	}
	if ops[1].Txn[1].F != MicroRead || !slices.Equal(ops[1].Txn[1].Values, []int64{1}) {
		t.Fatalf("unexpected micro-op 1: %+v", ops[1].Txn[1])
	}
}

func TestWriteAndReadEDNRoundTrip(t *testing.T) {
	orig := []Op{
		{Index: 0, Process: 0, Type: Invoke, F: FWrite, Key: "k0", Value: Int(10), Time: 100},
		{Index: 1, Process: 0, Type: OK, F: FWrite, Key: "k0", Value: Int(10), Time: 200},
		{Index: 2, Process: 1, Type: Invoke, F: FCas, Key: "k0", Old: Int(10), Value: Int(25), Time: 300},
		{Index: 3, Process: 1, Type: OK, F: FCas, Key: "k0", Old: Int(10), Value: Int(25), Time: 400},
		{
			Index: 4, Process: 2, Type: Invoke, F: FTxn, Key: "settlement", Value: Int(501), Time: 500,
			Txn: []MicroOp{{F: MicroAppend, Key: "ledger", Value: Int(501)}, {F: MicroAppend, Key: "broker", Value: Int(501)}},
		},
		{
			Index: 5, Process: 2, Type: OK, F: FTxn, Key: "settlement", Value: Int(501), Time: 600,
			Txn: []MicroOp{{F: MicroAppend, Key: "ledger", Value: Int(501)}, {F: MicroAppend, Key: "broker", Value: Int(501)}},
		},
		{
			Index: 6, Process: 3, Type: Invoke, F: FTxn, Key: "settlement", Time: 700,
			Txn: []MicroOp{{F: MicroRead, Key: "ledger"}, {F: MicroRead, Key: "broker"}},
		},
		{
			Index: 7, Process: 3, Type: OK, F: FTxn, Key: "settlement", Time: 800,
			Txn: []MicroOp{{F: MicroRead, Key: "ledger", Values: []int64{501}}, {F: MicroRead, Key: "broker", Values: []int64{501}}},
		},
	}

	var buf strings.Builder
	if err := WriteEDN(&buf, orig); err != nil {
		t.Fatalf("WriteEDN: %v", err)
	}
	roundTrip, err := ReadJSONL(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("ReadJSONL on WriteEDN output: %v\nEDN:\n%s", err, buf.String())
	}
	if Hash(orig) != Hash(roundTrip) {
		t.Fatalf("round-trip history hash mismatch:\norig=%+v\ngot =%+v\nEDN:\n%s", orig, roundTrip, buf.String())
	}
}

func TestReadEDNMaxDepth(t *testing.T) {
	deep := strings.Repeat("[", 512)
	if _, err := ReadEDN(strings.NewReader(deep)); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("expected maximum nesting depth error for brackets, got %v", err)
	}
	discards := strings.Repeat("#_", 512) + "{}"
	if _, err := ReadEDN(strings.NewReader(discards)); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("expected maximum nesting depth error for discards, got %v", err)
	}
}
