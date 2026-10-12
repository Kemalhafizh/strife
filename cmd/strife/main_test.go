package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kemalhafizh/strife/pkg/report"
)

func runCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String() + errb.String()
}

func TestSimWritesVerifiableReport(t *testing.T) {
	dir := t.TempDir()
	code, out := runCLI(t, "sim", "--system", "raft", "--seed", "3", "--duration", "3s", "--out", dir, "--determinism")
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "identical executions") || !strings.Contains(out, "LINEARIZABLE") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	rdir := filepath.Join(dir, "raft-seed3")
	for _, f := range []string{"report.json", "report.md", "report.html", "history.jsonl", "schedule.json"} {
		if _, err := os.Stat(filepath.Join(rdir, f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}

	// The fingerprint must be recomputable from the artifacts alone.
	data, err := os.ReadFile(filepath.Join(rdir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep report.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	if got := report.Fingerprint(&rep); got != rep.Fingerprint {
		t.Fatalf("fingerprint mismatch: %s vs %s", got, rep.Fingerprint)
	}

	// The exported history re-checks to the same verdict offline.
	code, out = runCLI(t, "check", "--history", filepath.Join(rdir, "history.jsonl"))
	if code != exitOK || !strings.Contains(out, "LINEARIZABLE") {
		t.Fatalf("offline check: exit %d\n%s", code, out)
	}

	// Replaying the exported schedule reproduces the run exactly.
	code, out2 := runCLI(t, "sim", "--system", "raft", "--seed", "3", "--duration", "3s", "--out", "", "--plan", filepath.Join(rdir, "schedule.json"))
	if code != exitOK || !strings.Contains(out2, rep.TraceHash[:16]) {
		t.Fatalf("plan replay diverged (exit %d), want trace %s:\n%s", code, rep.TraceHash[:16], out2)
	}
}

func TestSimExpectViolation(t *testing.T) {
	code, out := runCLI(t, "sim", "--system", "raft-stale-reads", "--faults", "partition", "--seeds", "1..12", "--out", "", "--expect", "violation")
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	code, _ = runCLI(t, "sim", "--system", "raft-stale-reads", "--faults", "partition", "--seeds", "1..12", "--out", "")
	if code != exitViolation {
		t.Fatalf("violations must exit %d, got %d", exitViolation, code)
	}
}

func TestPlanAndUsageErrors(t *testing.T) {
	code, out := runCLI(t, "plan", "--seed", "9", "--nodes", "3", "--faults", "partition,kill")
	if code != exitOK || !strings.Contains(out, "inject") {
		t.Fatalf("plan: exit %d\n%s", code, out)
	}
	for _, args := range [][]string{
		{"sim", "--system", "nope"},
		{"sim", "--faults", "meteor"},
		{"frobnicate"},
		{"check"},
	} {
		if code, _ := runCLI(t, args...); code != exitUsage {
			t.Errorf("%v: want usage error, got %d", args, code)
		}
	}
}

func TestLiveDryRun(t *testing.T) {
	dir := t.TempDir()
	code, out := runCLI(t, "run", "--config", "../../testdata/memory.json", "--duration", "1s", "--dry-run", "--quiet", "--out", dir)
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*", "commands.log"))
	if len(matches) != 1 {
		t.Fatalf("expected one commands.log, got %v", matches)
	}
}

func TestShrinkCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the simulator dozens of times")
	}
	dir := t.TempDir()
	// Find the violation, then shrink straight from its report.
	code, out := runCLI(t, "sim", "--system", "raft-stale-reads", "--faults", "partition", "--seed", "2", "--out", dir)
	if code != exitViolation {
		t.Fatalf("seed 2 should violate (exit %d):\n%s", code, out)
	}
	code, out = runCLI(t, "shrink", "--report", filepath.Join(dir, "raft-stale-reads-seed2", "report.json"), "--out", dir)
	if code != exitViolation {
		t.Fatalf("shrink: exit %d\n%s", code, out)
	}
	min := filepath.Join(dir, "raft-stale-reads-seed2-min")
	data, err := os.ReadFile(filepath.Join(min, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep report.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Shrink == nil || rep.Shrink.MinimalFaults > 3 || rep.Shrink.MinimalFaults >= rep.Shrink.OriginalFaults {
		t.Fatalf("bad shrink info: %+v", rep.Shrink)
	}
	if rep.Verdict != "violation" {
		t.Fatalf("minimal report verdict %s", rep.Verdict)
	}
	for _, f := range []string{"shrink.json", "schedule.json", "report.html"} {
		if _, err := os.Stat(filepath.Join(min, f)); err != nil {
			t.Fatalf("missing %s", f)
		}
	}
	md, _ := os.ReadFile(filepath.Join(min, "report.md"))
	if !strings.Contains(string(md), "## Shrinking") {
		t.Fatal("markdown report lacks the shrinking section")
	}

	// The printed reproduction command replays the minimal run exactly.
	args := strings.Fields(strings.TrimPrefix(rep.Reproduce, "strife "))
	code, out = runCLI(t, append(args, "--out", "")...)
	if code != exitViolation || !strings.Contains(out, rep.TraceHash[:16]) {
		t.Fatalf("reproduce diverged (exit %d), want trace %s:\n%s", code, rep.TraceHash[:16], out)
	}

	// A passing scenario is a usage error, not a crash.
	code, _ = runCLI(t, "shrink", "--system", "raft", "--seed", "1", "--duration", "2s", "--out", "")
	if code != exitUsage {
		t.Fatalf("shrinking a passing run should exit %d, got %d", exitUsage, code)
	}
}
