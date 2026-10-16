package txn

import (
	"fmt"

	"github.com/kemalhafizh/strife/pkg/history"
)

// AnomalyType classifies formal transactional anomalies according to Adya (1999) and Elle.
type AnomalyType string

const (
	// G0: Dirty Write. A cycle consisting purely of write-write (ww) dependencies.
	AnomalyG0 AnomalyType = "G0"

	// G1a: Aborted Read. A committed transaction read data written by an aborted transaction.
	AnomalyG1a AnomalyType = "G1a"

	// G1b: Intermediate Read. A transaction read an intermediate write from another
	// transaction that was subsequently overwritten before commitment.
	AnomalyG1b AnomalyType = "G1b"

	// G1c: Circular Information Flow. A cycle consisting of wr, ww, and so dependencies.
	// Required to be prevented for Read Committed.
	AnomalyG1c AnomalyType = "G1c"

	// G2: Anti-dependency Cycle. A cycle in the serialization graph containing at least
	// one read-write (rw) anti-dependency. Proves non-serializability.
	AnomalyG2 AnomalyType = "G2"

	// GRealtime: Real-time order violation. A cycle in the serialization graph that
	// incorporates real-time order (ro) edges. Proves non-strict serializability.
	AnomalyGRealtime AnomalyType = "G-realtime"
)

// Anomaly describes an observed isolation violation with its witnessing cycle or transactions.
type Anomaly struct {
	Type        AnomalyType `json:"type"`
	Name        string      `json:"name"`
	Explanation string      `json:"explanation"`
	Cycle       *Cycle      `json:"cycle,omitempty"`
	Txns        []Txn       `json:"txns,omitempty"`
}

// String formats the anomaly for human consumption.
func (a Anomaly) String() string {
	if a.Cycle != nil {
		return fmt.Sprintf("[%s] %s: %s\n  Cycle: %s", a.Type, a.Name, a.Explanation, a.Cycle.String())
	}
	return fmt.Sprintf("[%s] %s: %s", a.Type, a.Name, a.Explanation)
}

// DetectG0 detects Dirty Write cycles (cycles composed purely of ww edges).
func DetectG0(g *Graph) []Anomaly {
	sub := g.Subgraph(EdgeWW)
	cycles := FindCycles(sub)
	var anomalies []Anomaly
	for _, c := range cycles {
		anomalies = append(anomalies, Anomaly{
			Type:        AnomalyG0,
			Name:        "Dirty Write (G0)",
			Explanation: "Found a cycle of write-write dependencies. Overwriting concurrent uncommitted writes breaks consistency.",
			Cycle:       &c,
		})
	}
	return anomalies
}

// DetectG1a detects Aborted Reads: a committed transaction that read data written by an aborted transaction.
func DetectG1a(txns []Txn) []Anomaly {
	// Index writes of aborted transactions: key -> val -> Txn
	abortedWrites := make(map[string]map[int64]Txn)
	for _, t := range txns {
		if t.Status == history.Fail {
			for _, op := range t.Ops {
				if op.Kind == OpWrite && op.Value != nil {
					k := op.Key
					v := *op.Value
					if abortedWrites[k] == nil {
						abortedWrites[k] = make(map[int64]Txn)
					}
					abortedWrites[k][v] = t
				}
			}
		}
	}

	var anomalies []Anomaly
	for _, t := range txns {
		if t.Status != history.OK {
			continue
		}
		for _, op := range t.Ops {
			if op.Kind == OpRead && op.Value != nil {
				k := op.Key
				v := *op.Value
				if ab, ok := abortedWrites[k][v]; ok {
					anomalies = append(anomalies, Anomaly{
						Type:        AnomalyG1a,
						Name:        "Aborted Read (G1a)",
						Explanation: fmt.Sprintf("Transaction T%d read key %s=%d from aborted transaction T%d.", t.ID, k, v, ab.ID),
						Txns:        []Txn{ab, t},
					})
				}
			}
		}
	}
	return anomalies
}

// DetectG1b detects Intermediate Reads: a transaction read an intermediate write
// of another transaction that was later modified by that same transaction.
func DetectG1b(txns []Txn) []Anomaly {
	// Map of intermediate writes: key -> val -> {Txn, FinalVal}
	type interWrite struct {
		txn      Txn
		finalVal int64
	}
	inter := make(map[string]map[int64]interWrite)

	for _, t := range txns {
		// Group writes per key within transaction
		writesPerKey := make(map[string][]int64)
		for _, op := range t.Ops {
			if op.Kind == OpWrite && op.Value != nil {
				writesPerKey[op.Key] = append(writesPerKey[op.Key], *op.Value)
			}
		}
		for k, vals := range writesPerKey {
			if len(vals) > 1 {
				finalVal := vals[len(vals)-1]
				// All writes except the last are intermediate
				for _, iv := range vals[:len(vals)-1] {
					if iv != finalVal {
						if inter[k] == nil {
							inter[k] = make(map[int64]interWrite)
						}
						inter[k][iv] = interWrite{txn: t, finalVal: finalVal}
					}
				}
			}
		}
	}

	var anomalies []Anomaly
	for _, reader := range txns {
		for _, op := range reader.Ops {
			if op.Kind == OpRead && op.Value != nil {
				k := op.Key
				v := *op.Value
				if iw, ok := inter[k][v]; ok && reader.ID != iw.txn.ID {
					anomalies = append(anomalies, Anomaly{
						Type:        AnomalyG1b,
						Name:        "Intermediate Read (G1b)",
						Explanation: fmt.Sprintf("Transaction T%d read intermediate value %s=%d from T%d, which was overwritten with %d before commit.", reader.ID, k, v, iw.txn.ID, iw.finalVal),
						Txns:        []Txn{iw.txn, reader},
					})
				}
			}
		}
	}
	return anomalies
}

// DetectG1c detects Circular Information Flow: cycles in (wr ∪ ww ∪ so).
func DetectG1c(g *Graph) []Anomaly {
	sub := g.Subgraph(EdgeWR, EdgeWW, EdgeSO)
	cycles := FindCycles(sub)
	var anomalies []Anomaly
	for _, c := range cycles {
		// Only report as G1c if it contains at least one WR edge (pure WW is G0)
		if c.HasKind(EdgeWR) {
			anomalies = append(anomalies, Anomaly{
				Type:        AnomalyG1c,
				Name:        "Circular Information Flow (G1c)",
				Explanation: "Found a circular dependency of writes and reads. Transactions observed each other's effects in a cycle.",
				Cycle:       &c,
			})
		}
	}
	return anomalies
}

// DetectG2 detects Anti-dependency Cycles (G2 / Serializability violation).
// These are cycles in (wr ∪ ww ∪ rw ∪ so) that contain at least one rw edge.
func DetectG2(g *Graph) []Anomaly {
	sub := g.Subgraph(EdgeWR, EdgeWW, EdgeRW, EdgeSO)
	cycles := FindCycles(sub)
	var anomalies []Anomaly
	for _, c := range cycles {
		if c.HasKind(EdgeRW) {
			anomalies = append(anomalies, Anomaly{
				Type:        AnomalyG2,
				Name:        "Anti-dependency Cycle (G2)",
				Explanation: "Found a cycle containing anti-dependencies (rw). No serial execution order can explain this history.",
				Cycle:       &c,
			})
		}
	}
	return anomalies
}

// DetectGRealtime detects Real-Time Inversions (Strict Serializability violation).
// These are cycles in (wr ∪ ww ∪ rw ∪ so ∪ ro) that contain at least one ro edge.
func DetectGRealtime(g *Graph) []Anomaly {
	sub := g.Subgraph(EdgeWR, EdgeWW, EdgeRW, EdgeSO, EdgeRO)
	cycles := FindCycles(sub)
	var anomalies []Anomaly
	for _, c := range cycles {
		if c.HasKind(EdgeRO) {
			anomalies = append(anomalies, Anomaly{
				Type:        AnomalyGRealtime,
				Name:        "Real-Time Inversion (G-realtime)",
				Explanation: "Found a dependency cycle contradicting real-time order. A completed transaction was ordered after a subsequent one.",
				Cycle:       &c,
			})
		}
	}
	return anomalies
}
