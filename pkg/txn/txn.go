// Package txn provides transactional consistency and isolation anomaly
// detection in the spirit of Kyle Kingsbury's Elle and Adya's weak isolation
// formalisation.
//
// Unlike single-key linearizability checking (which checks individual register
// histories against a sequential model), Elle analyzes multi-key transactions
// by building a Direct Serialization Graph (DSG) of dependencies:
//
//   - wr (write-read): T1 writes value v to key k, and T2 reads that value v;
//   - ww (write-write): T1 writes key k, and T2 overwrites it with a later version;
//   - rw (read-write / anti-dependency): T1 reads a version of key k, and T2 writes a later version;
//   - so (session order): T1 precedes T2 on the same client process;
//   - ro (real-time order): T1 completed before T2 was invoked.
//
// Cycles and dependency rules identify standard ANSI/Adya isolation anomalies:
//
//   - G0  (Dirty Write): a cycle consisting purely of ww dependencies;
//   - G1a (Aborted Read): a committed transaction read a value written by an aborted transaction;
//   - G1b (Intermediate Read): a transaction read an intermediate, uncommitted state of another transaction;
//   - G1c (Circular Information Flow): a cycle consisting of wr, ww, and so dependencies;
//   - G2  (Anti-dependency Cycle): a cycle in the DSG containing rw dependencies (violates Serializability);
//   - G-realtime (Real-time Inversion): a cycle involving ro edges (violates Strict Serializability).
package txn

import (
	"fmt"
	"strings"

	"github.com/kemalhafizh/strife/pkg/history"
)

// OpKind is the kind of a transactional micro-operation ("r" for read, "w" for write).
type OpKind string

const (
	OpRead  OpKind = "r"
	OpWrite OpKind = "w"
)

// MicroOp is an individual read or write operation inside a transaction.
type MicroOp struct {
	Kind  OpKind `json:"f"`               // "r" or "w"
	Key   string `json:"key"`             // target key
	Value *int64 `json:"value,omitempty"` // written or read value
}

// String formats a micro-operation, e.g. "r(x)→1" or "w(y, 2)".
func (m MicroOp) String() string {
	val := "nil"
	if m.Value != nil {
		val = fmt.Sprint(*m.Value)
	}
	if m.Kind == OpWrite {
		return fmt.Sprintf("w(%s, %s)", m.Key, val)
	}
	if m.Value != nil {
		return fmt.Sprintf("r(%s)→%s", m.Key, val)
	}
	return fmt.Sprintf("r(%s)", m.Key)
}

// Txn represents a single transaction composed of one or more micro-operations.
type Txn struct {
	ID      int          `json:"id"`
	Process int          `json:"process"`
	Status  history.Type `json:"status"` // history.OK, history.Fail, history.Info
	Call    int64        `json:"call"`   // start time in nanoseconds
	Return  int64        `json:"return"` // finish time in nanoseconds
	Ops     []MicroOp    `json:"ops"`    // sequence of micro-operations
}

// Describe returns a compact, human-readable summary of the transaction,
// e.g. "T1 [p0] ok: [r(x)→1, w(y, 2)]".
func (t Txn) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "T%d (p%d) %s: [", t.ID, t.Process, t.Status)
	for i, o := range t.Ops {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(o.String())
	}
	b.WriteString("]")
	return b.String()
}

// FromOperations converts paired history operations into a slice of transactions.
// It handles operations where F == "txn" or where individual operations are treated
// as single-op transactions.
func FromOperations(paired []history.Operation) []Txn {
	txns := make([]Txn, len(paired))
	for i, p := range paired {
		t := Txn{
			ID:      p.ID,
			Process: p.Process,
			Status:  p.Status(),
			Call:    p.Call,
			Return:  p.Return,
		}
		if p.Input.F == history.FTxn && len(p.Input.Txn) > 0 {
			// Read results live on the completion (p.Micro), not on the
			// invocation: reading them from the invocation would turn every
			// read into a read of the initial state.
			for _, m := range p.Micro() {
				kind := OpRead
				if m.F == "w" || m.F == "write" {
					kind = OpWrite
				}
				t.Ops = append(t.Ops, MicroOp{
					Kind:  kind,
					Key:   m.Key,
					Value: m.Value,
				})
			}
		} else {
			// Convert single operation to micro-op
			kind := OpRead
			if p.Input.F == history.FWrite || p.Input.F == history.FCas || p.Input.F == history.FAdd {
				kind = OpWrite
			}
			val := p.Input.Value
			if kind == OpRead && p.Output.Value != nil {
				val = p.Output.Value
			}
			t.Ops = append(t.Ops, MicroOp{
				Kind:  kind,
				Key:   p.Input.Key,
				Value: val,
			})
		}
		txns[i] = t
	}
	return txns
}
