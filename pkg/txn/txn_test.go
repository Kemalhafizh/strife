package txn

import (
	"testing"

	"github.com/kemalhafizh/strife/pkg/history"
)

var v = history.Int

func TestSerializableHistoryPass(t *testing.T) {
	txns := []Txn{
		{
			ID:      0,
			Process: 0,
			Status:  history.OK,
			Call:    0,
			Return:  10,
			Ops: []MicroOp{
				{Kind: OpWrite, Key: "x", Value: v(1)},
				{Kind: OpWrite, Key: "y", Value: v(1)},
			},
		},
		{
			ID:      1,
			Process: 1,
			Status:  history.OK,
			Call:    15,
			Return:  25,
			Ops: []MicroOp{
				{Kind: OpRead, Key: "x", Value: v(1)},
				{Kind: OpRead, Key: "y", Value: v(1)},
			},
		},
		{
			ID:      2,
			Process: 0,
			Status:  history.OK,
			Call:    30,
			Return:  40,
			Ops: []MicroOp{
				{Kind: OpWrite, Key: "x", Value: v(2)},
				{Kind: OpWrite, Key: "y", Value: v(2)},
			},
		},
		{
			ID:      3,
			Process: 1,
			Status:  history.OK,
			Call:    45,
			Return:  55,
			Ops: []MicroOp{
				{Kind: OpRead, Key: "x", Value: v(2)},
				{Kind: OpRead, Key: "y", Value: v(2)},
			},
		},
	}

	rep := Check(txns, LevelSerializable)
	if rep.Verdict != Pass {
		t.Fatalf("expected pass for serializable history, got: %s\n%s", rep.Verdict, rep.FullReport())
	}
	if len(rep.Anomalies) > 0 {
		t.Fatalf("expected no anomalies, got %d", len(rep.Anomalies))
	}
}

func TestG0DirtyWriteCycle(t *testing.T) {
	// T1 writes x=1, y=2
	// T2 writes x=2, y=1
	// T3 reads x=1, then T4 reads x=2 (establishing x: T1 -> T2)
	// T5 reads y=1, then T6 reads y=2 (establishing y: T2 -> T1)
	txns := []Txn{
		{
			ID:     1,
			Status: history.OK,
			Call:   0,
			Return: 10,
			Ops: []MicroOp{
				{Kind: OpWrite, Key: "x", Value: v(1)},
				{Kind: OpWrite, Key: "y", Value: v(2)},
			},
		},
		{
			ID:     2,
			Status: history.OK,
			Call:   0,
			Return: 10,
			Ops: []MicroOp{
				{Kind: OpWrite, Key: "x", Value: v(2)},
				{Kind: OpWrite, Key: "y", Value: v(1)},
			},
		},
		{
			ID:     3,
			Status: history.OK,
			Call:   15,
			Return: 20,
			Ops:    []MicroOp{{Kind: OpRead, Key: "x", Value: v(1)}},
		},
		{
			ID:     4,
			Status: history.OK,
			Call:   25,
			Return: 30,
			Ops:    []MicroOp{{Kind: OpRead, Key: "x", Value: v(2)}},
		},
		{
			ID:     5,
			Status: history.OK,
			Call:   35,
			Return: 40,
			Ops:    []MicroOp{{Kind: OpRead, Key: "y", Value: v(1)}},
		},
		{
			ID:     6,
			Status: history.OK,
			Call:   45,
			Return: 50,
			Ops:    []MicroOp{{Kind: OpRead, Key: "y", Value: v(2)}},
		},
	}

	rep := Check(txns, LevelReadUncommitted)
	if rep.Verdict != Violation {
		t.Fatal("expected G0 dirty write violation")
	}
	found := false
	for _, a := range rep.Anomalies {
		if a.Type == AnomalyG0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected G0 anomaly in report: %+v", rep.Anomalies)
	}
}

func TestG1aAbortedRead(t *testing.T) {
	txns := []Txn{
		{
			ID:     1,
			Status: history.Fail, // Aborted
			Call:   0,
			Return: 10,
			Ops: []MicroOp{
				{Kind: OpWrite, Key: "x", Value: v(42)},
			},
		},
		{
			ID:     2,
			Status: history.OK, // Committed
			Call:   15,
			Return: 25,
			Ops: []MicroOp{
				{Kind: OpRead, Key: "x", Value: v(42)},
			},
		},
	}

	rep := Check(txns, LevelReadCommitted)
	if rep.Verdict != Violation {
		t.Fatal("expected G1a aborted read violation")
	}
	found := false
	for _, a := range rep.Anomalies {
		if a.Type == AnomalyG1a {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected G1a anomaly, got: %+v", rep.Anomalies)
	}
}

func TestG1bIntermediateRead(t *testing.T) {
	txns := []Txn{
		{
			ID:     1,
			Status: history.OK,
			Call:   0,
			Return: 20,
			Ops: []MicroOp{
				{Kind: OpWrite, Key: "x", Value: v(10)}, // intermediate
				{Kind: OpWrite, Key: "x", Value: v(20)}, // final committed
			},
		},
		{
			ID:     2,
			Status: history.OK,
			Call:   25,
			Return: 35,
			Ops: []MicroOp{
				{Kind: OpRead, Key: "x", Value: v(10)}, // read intermediate!
			},
		},
	}

	rep := Check(txns, LevelReadCommitted)
	if rep.Verdict != Violation {
		t.Fatal("expected G1b intermediate read violation")
	}
	found := false
	for _, a := range rep.Anomalies {
		if a.Type == AnomalyG1b {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected G1b anomaly, got: %+v", rep.Anomalies)
	}
}

func TestG1cCircularInformationFlow(t *testing.T) {
	// T1 writes x=1 and reads y=2
	// T2 writes y=2 and reads x=1
	// T1 ──wr(x=1)──> T2 ──wr(y=2)──> T1
	txns := []Txn{
		{
			ID:     1,
			Status: history.OK,
			Call:   0,
			Return: 10,
			Ops: []MicroOp{
				{Kind: OpWrite, Key: "x", Value: v(1)},
				{Kind: OpRead, Key: "y", Value: v(2)},
			},
		},
		{
			ID:     2,
			Status: history.OK,
			Call:   0,
			Return: 10,
			Ops: []MicroOp{
				{Kind: OpWrite, Key: "y", Value: v(2)},
				{Kind: OpRead, Key: "x", Value: v(1)},
			},
		},
	}

	rep := Check(txns, LevelReadCommitted)
	if rep.Verdict != Violation {
		t.Fatal("expected G1c circular information flow violation")
	}
	found := false
	for _, a := range rep.Anomalies {
		if a.Type == AnomalyG1c {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected G1c anomaly, got: %+v", rep.Anomalies)
	}
}

func TestG2WriteSkew(t *testing.T) {
	// Classic Write Skew under Snapshot Isolation (allowed under SI, forbidden under Serializable)
	// Initial state: x=1, y=1 (written by T0)
	// T1: reads x=1, reads y=1, writes x=0
	// T2: reads x=1, reads y=1, writes y=0
	// Dependencies:
	// T0 ──ww(x)──> T1
	// T0 ──ww(y)──> T2
	// T1 reads y=1 (T0) while T2 writes later y=0  =>  T1 ──rw(y)──> T2
	// T2 reads x=1 (T0) while T1 writes later x=0  =>  T2 ──rw(x)──> T1
	// Cycle: T1 ──rw(y)──> T2 ──rw(x)──> T1
	txns := []Txn{
		{
			ID:     0,
			Status: history.OK,
			Call:   0,
			Return: 5,
			Ops: []MicroOp{
				{Kind: OpWrite, Key: "x", Value: v(1)},
				{Kind: OpWrite, Key: "y", Value: v(1)},
			},
		},
		{
			ID:     1,
			Status: history.OK,
			Call:   10,
			Return: 20,
			Ops: []MicroOp{
				{Kind: OpRead, Key: "x", Value: v(1)},
				{Kind: OpRead, Key: "y", Value: v(1)},
				{Kind: OpWrite, Key: "x", Value: v(0)},
			},
		},
		{
			ID:     2,
			Status: history.OK,
			Call:   10,
			Return: 20,
			Ops: []MicroOp{
				{Kind: OpRead, Key: "x", Value: v(1)},
				{Kind: OpRead, Key: "y", Value: v(1)},
				{Kind: OpWrite, Key: "y", Value: v(0)},
			},
		},
	}

	// Under Read Committed / Snapshot Isolation:
	repSI := Check(txns, LevelSnapshotIsolation)
	if repSI.Verdict != Pass {
		t.Fatalf("Write Skew should pass Snapshot Isolation, got: %s", repSI.Verdict)
	}

	// Under Serializable: G2 anti-dependency cycle must be detected and rejected!
	repSer := Check(txns, LevelSerializable)
	if repSer.Verdict != Violation {
		t.Fatal("expected G2 violation under Serializable")
	}
	found := false
	for _, a := range repSer.Anomalies {
		if a.Type == AnomalyG2 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected G2 anomaly, got: %+v", repSer.Anomalies)
	}
}

func TestGRealtimeViolation(t *testing.T) {
	// Stale read across real-time:
	// T0 writes x=1 (0..5)
	// T1 writes x=2 (10..15) -> completes strictly before T2 begins!
	// T2 reads x=1 (20..25)  -> reads stale version from T0 instead of T1!
	// Real-time order: T1 ──ro──> T2
	// Anti-dependency: T2 reads T0's x=1 while T1 wrote later x=2  =>  T2 ──rw(x)──> T1
	// Cycle: T1 ──ro──> T2 ──rw(x)──> T1
	txns := []Txn{
		{
			ID:     0,
			Status: history.OK,
			Call:   0,
			Return: 5,
			Ops: []MicroOp{
				{Kind: OpWrite, Key: "x", Value: v(1)},
			},
		},
		{
			ID:     1,
			Status: history.OK,
			Call:   10,
			Return: 15,
			Ops: []MicroOp{
				{Kind: OpWrite, Key: "x", Value: v(2)},
			},
		},
		{
			ID:     2,
			Status: history.OK,
			Call:   20,
			Return: 25,
			Ops: []MicroOp{
				{Kind: OpRead, Key: "x", Value: v(1)},
			},
		},
	}

	// Without real-time constraints (pure Serializability), a serial order T2 -> T0 -> T1 or T0 -> T2 -> T1 exists
	// (T2 ran before T1 in logical serialization order).
	// Under Strict Serializability (Linearizability), real-time order T1 ->ro T2 forbids this!
	repStrict := Check(txns, LevelStrictSerializable)
	if repStrict.Verdict != Violation {
		t.Fatal("expected G-realtime violation under Strict Serializable")
	}
	found := false
	for _, a := range repStrict.Anomalies {
		if a.Type == AnomalyGRealtime {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected G-realtime anomaly, got: %+v", repStrict.Anomalies)
	}
}
