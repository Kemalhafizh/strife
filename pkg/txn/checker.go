package txn

import (
	"fmt"
	"strings"
)

// Verdict is the outcome of an isolation check.
type Verdict string

const (
	Pass      Verdict = "pass"
	Violation Verdict = "violation"
)

// IsolationLevel defines the target transactional isolation guarantee.
type IsolationLevel string

const (
	LevelReadUncommitted    IsolationLevel = "read-uncommitted"
	LevelReadCommitted      IsolationLevel = "read-committed"
	LevelRepeatableRead     IsolationLevel = "repeatable-read"
	LevelSnapshotIsolation  IsolationLevel = "snapshot-isolation"
	LevelSerializable       IsolationLevel = "serializable"
	LevelStrictSerializable IsolationLevel = "strict-serializable"
)

// Report contains the results of isolation checking.
type Report struct {
	Verdict      Verdict        `json:"verdict"` // Pass or Violation
	Level        IsolationLevel `json:"level"`
	Transactions int            `json:"transactions"`
	Operations   int            `json:"operations"`
	Edges        int            `json:"edges"`
	Anomalies    []Anomaly      `json:"anomalies,omitempty"`
}

// Summary returns a concise one-line description of the result.
func (r *Report) Summary() string {
	if r.Verdict == Pass {
		return fmt.Sprintf("OK: %d transactions satisfy %s isolation (%d edges checked).",
			r.Transactions, r.Level, r.Edges)
	}
	if len(r.Anomalies) > 0 {
		first := r.Anomalies[0]
		return fmt.Sprintf("VIOLATION (%s): %s", first.Type, first.Explanation)
	}
	return "VIOLATION: isolation constraints not satisfied."
}

// FullReport formats a complete human-readable report of the isolation check.
func (r *Report) FullReport() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Transactional Isolation Report (%s)\n", r.Level)
	fmt.Fprintf(&b, "Transactions: %d | Micro-ops: %d | Dependency Edges: %d\n", r.Transactions, r.Operations, r.Edges)
	fmt.Fprintf(&b, "Verdict: %s\n\n", strings.ToUpper(string(r.Verdict)))

	if len(r.Anomalies) == 0 {
		b.WriteString("No isolation anomalies detected.\n")
		return b.String()
	}

	fmt.Fprintf(&b, "Detected %d Anomaly(s):\n", len(r.Anomalies))
	for i, a := range r.Anomalies {
		fmt.Fprintf(&b, "\n%d. [%s] %s\n", i+1, a.Type, a.Name)
		fmt.Fprintf(&b, "   Explanation: %s\n", a.Explanation)
		if a.Cycle != nil {
			fmt.Fprintf(&b, "   Cycle: %s\n", a.Cycle.String())
		}
		if len(a.Txns) > 0 {
			b.WriteString("   Transactions:\n")
			for _, t := range a.Txns {
				fmt.Fprintf(&b, "     - %s\n", t.Describe())
			}
		}
	}
	return b.String()
}

// Check evaluates a set of transactions against a desired isolation level.
func Check(txns []Txn, level IsolationLevel) *Report {
	if level == "" {
		level = LevelSerializable
	}

	totalOps := 0
	for _, t := range txns {
		totalOps += len(t.Ops)
	}

	g := buildGraph(txns, level == LevelStrictSerializable)
	rep := &Report{
		Verdict:      Pass,
		Level:        level,
		Transactions: len(txns),
		Operations:   totalOps,
		Edges:        len(g.Edges),
	}

	var anomalies []Anomaly

	// 1. All levels must prevent Dirty Writes (G0)
	anomalies = append(anomalies, DetectG0(g)...)

	// 2. Read Committed and above must prevent Aborted Reads (G1a),
	// Intermediate Reads (G1b), and Circular Information Flow (G1c)
	if level != LevelReadUncommitted {
		anomalies = append(anomalies, DetectG1a(txns)...)
		anomalies = append(anomalies, DetectG1b(txns)...)
		anomalies = append(anomalies, DetectG1c(g)...)
	}

	// 3. Serializable and above must prevent Anti-dependency cycles (G2)
	if level == LevelSerializable || level == LevelStrictSerializable {
		anomalies = append(anomalies, DetectG2(g)...)
	}

	// 4. Strict Serializability must also prevent Real-Time Inversions (G-realtime)
	if level == LevelStrictSerializable {
		anomalies = append(anomalies, DetectGRealtime(g)...)
	}

	if len(anomalies) > 0 {
		rep.Verdict = Violation
		rep.Anomalies = anomalies
	}

	return rep
}
