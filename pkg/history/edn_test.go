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
