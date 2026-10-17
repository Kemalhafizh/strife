package probe

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

func TestParsePSITotalAndCgroupOOM(t *testing.T) {
	psi := "some avg10=12.50 avg60=3.10 avg300=1.05 total=450000\nfull avg10=8.00 avg60=2.00 avg300=0.50 total=220000\n"
	some, full, ok := ParsePSITotal(psi)
	if !ok || some != 450000 || full != 220000 {
		t.Fatalf("unexpected PSI parse: some=%d full=%d ok=%v", some, full, ok)
	}

	mem := "low 0\nhigh 0\nmax 2\noom 1\noom_kill 3\noom_group_kill 0\n"
	kills, ok := ParseCgroupOOMKills(mem)
	if !ok || kills != 3 {
		t.Fatalf("unexpected cgroup oom_kill parse: kills=%d ok=%v", kills, ok)
	}
}

func TestCollectorPSIOOMAndJSONL(t *testing.T) {
	tmp := t.TempDir()
	procDir := filepath.Join(tmp, "proc")
	pressureDir := filepath.Join(procDir, "pressure")
	cgroupDir := filepath.Join(tmp, "cgroup")
	canaryDir := filepath.Join(tmp, "canary")

	for _, d := range []string{pressureDir, cgroupDir, canaryDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	ioFile := filepath.Join(pressureDir, "io")
	memFile := filepath.Join(cgroupDir, "memory.events")

	// Baseline sample
	_ = os.WriteFile(ioFile, []byte("some avg10=0.00 total=10000\nfull avg10=0.00 total=5000\n"), 0o644)
	_ = os.WriteFile(memFile, []byte("oom 0\noom_kill 0\n"), 0o644)

	col := NewCollector(CollectorConfig{
		Node:           "n0",
		ProcRoot:       procRoot(procDir),
		CgroupDir:      cgroupDir,
		CanaryDir:      canaryDir,
		PSIThreshold:   20 * time.Millisecond,
		FsyncThreshold: 10 * time.Hour,
	})
	col.SampleOnce()
	if len(col.Events()) != 0 {
		t.Fatalf("expected 0 events on initial baseline sample, got %d", len(col.Events()))
	}

	// Second sample with +150ms IO full stall and +1 OOM kill
	_ = os.WriteFile(ioFile, []byte("some avg10=5.00 total=180000\nfull avg10=4.00 total=155000\n"), 0o644)
	_ = os.WriteFile(memFile, []byte("oom 1\noom_kill 1\n"), 0o644)
	col.SampleOnce()

	evs := col.Events()
	if len(evs) != 2 {
		t.Fatalf("expected 2 probe events (io_pressure and oom_kill), got %+v", evs)
	}

	var buf bytes.Buffer
	if err := WriteEvents(&buf, evs); err != nil {
		t.Fatalf("WriteEvents: %v", err)
	}
	roundTrip, err := ReadEvents(&buf)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(roundTrip) != 2 {
		t.Fatalf("expected 2 round-tripped events, got %d", len(roundTrip))
	}
}

func procRoot(p string) string { return p }

func TestCorrelateViolationWithServerStall(t *testing.T) {
	sec := int64(time.Second)
	ops := []history.Op{
		{Index: 0, Process: 0, Type: history.Invoke, F: history.FWrite, Key: "x", Value: history.Int(1), Time: 1 * sec},
		{Index: 1, Process: 0, Type: history.OK, F: history.FWrite, Key: "x", Value: history.Int(1), Time: 2 * sec},
		{Index: 2, Process: 1, Type: history.Invoke, F: history.FWrite, Key: "x", Value: history.Int(2), Time: 3 * sec},
		{Index: 3, Process: 1, Type: history.OK, F: history.FWrite, Key: "x", Value: history.Int(2), Time: 4 * sec},
		{Index: 4, Process: 2, Type: history.Invoke, F: history.FRead, Key: "x", Time: 5 * sec},
		{Index: 5, Process: 2, Type: history.OK, F: history.FRead, Key: "x", Value: history.Int(1), Time: 6 * sec},
	}

	rep, err := checker.Check(model.KV{}, ops, checker.Options{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep.Verdict != checker.Violation {
		t.Fatalf("expected violation, got %s", rep.Verdict)
	}
	_ = checker.AddWitnesses(model.KV{}, ops, &rep, checker.WitnessOptions{})

	sched := nemesis.Schedule{
		Nodes:    3,
		Duration: 10 * time.Second,
		Faults: []nemesis.Fault{
			{ID: 1, Kind: nemesis.IOStall, Nodes: []int{0}, Start: 2500 * time.Millisecond, End: 6500 * time.Millisecond},
		},
	}

	events := []Event{
		{
			Node:       "n0",
			Kind:       KindFsyncStall,
			Start:      2800 * int64(time.Millisecond),
			End:        5100 * int64(time.Millisecond),
			DurationNs: 2300 * int64(time.Millisecond),
			Detail:     "fdatasync WAL stalled",
		},
		{
			Node:       "n1",
			Kind:       KindClockJump,
			Start:      4900 * int64(time.Millisecond),
			End:        4900 * int64(time.Millisecond),
			DurationNs: 450 * int64(time.Millisecond),
			Detail:     "wall-clock jump +450ms",
		},
	}

	findings := Correlate(ops, rep, sched, events)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	f := findings[0]
	if f.Key != "x" {
		t.Errorf("expected key x, got %s", f.Key)
	}
	if len(f.Events) != 2 {
		t.Fatalf("expected 2 correlated probe events, got %+v", f.Events)
	}
	if f.Events[0].Kind != KindFsyncStall {
		t.Errorf("expected highest-severity event to be fsync_stall, got %s", f.Events[0].Kind)
	}
	if !strings.Contains(f.Hypothesis, "n0: fsync stalled for 2.3s") {
		t.Errorf("expected hypothesis to explain fsync stall, got: %s", f.Hypothesis)
	}
}
