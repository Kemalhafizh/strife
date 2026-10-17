package txn

import (
	"os"
	"sort"
	"testing"

	"github.com/kemalhafizh/strife/pkg/history"
)

// Histories recorded on real systems (see testdata/anomalies/README.md) must
// keep producing the verdicts they produced there.
func TestPostgresSerializableG2(t *testing.T) {
	f, err := os.Open("../../testdata/anomalies/postgres-17.11-serializable-g2.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	raw, err := history.ReadJSONL(f)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := history.Pair(raw)
	if err != nil {
		t.Fatal(err)
	}

	if rep := CheckAppend(ops, LevelSnapshotIsolation); rep.Verdict != Pass {
		t.Fatalf("snapshot isolation: %s", rep.Summary())
	}
	for _, level := range []IsolationLevel{LevelSerializable, LevelStrictSerializable} {
		rep := CheckAppend(ops, level)
		if rep.Verdict != Violation || len(rep.Anomalies) == 0 {
			t.Fatalf("%s: want a violation, got %s", level, rep.Summary())
		}
		a := rep.Anomalies[0]
		if a.Type != AnomalyG2 {
			t.Fatalf("%s: want G2, got %s", level, a)
		}
		var procs []int
		for _, x := range a.Txns {
			procs = append(procs, x.Process)
		}
		sort.Ints(procs)
		if len(procs) != 3 || procs[0] != 40 || procs[1] != 43 || procs[2] != 49 {
			t.Fatalf("%s: want the cycle of p40, p43 and p49, got processes %v:\n%s", level, procs, a)
		}
	}
}
