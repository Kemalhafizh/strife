package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
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

type junitDoc struct {
	Tests    int `xml:"tests,attr"`
	Failures int `xml:"failures,attr"`
	Errors   int `xml:"errors,attr"`
	Cases    []struct {
		Name    string    `xml:"name,attr"`
		Failure *struct{} `xml:"failure"`
		Error   *struct{} `xml:"error"`
	} `xml:"testsuite>testcase"`
}

func readJUnit(t *testing.T, path string) junitDoc {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d junitDoc
	if err := xml.Unmarshal(b, &d); err != nil {
		t.Fatalf("bad JUnit: %v\n%s", err, b)
	}
	return d
}

func TestCIOutputs(t *testing.T) {
	dir := t.TempDir()
	j, sum := filepath.Join(dir, "j.xml"), filepath.Join(dir, "summary.md")

	// A single violating seed: one failing case per broken key.
	code, out := runCLI(t, "sim", "--system", "raft-stale-reads", "--faults", "partition", "--seed", "2", "--out", dir, "--junit", j, "--summary", sum)
	if code != exitViolation {
		t.Fatalf("exit %d\n%s", code, out)
	}
	d := readJUnit(t, j)
	if d.Failures == 0 || d.Failures != d.Tests {
		t.Fatalf("every key of seed 2 violates, got %+v", d)
	}

	// Bug hunting: finding the violation is the test, so it passes.
	code, out = runCLI(t, "sim", "--system", "raft-stale-reads", "--faults", "partition", "--seeds", "1..6", "--out", "", "--expect", "violation", "--junit", j, "--summary", sum)
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if d := readJUnit(t, j); d.Tests != 1 || d.Failures != 0 || d.Cases[0].Name != "finds a violation" {
		t.Fatalf("bug hunt should be one passing case, got %+v", d)
	}

	// Single seed with an expectation: the expectation is the test case.
	code, _ = runCLI(t, "sim", "--system", "raft-stale-reads", "--faults", "partition", "--seed", "2", "--out", "", "--expect", "violation", "--junit", j)
	if d := readJUnit(t, j); code != exitOK || d.Tests != 1 || d.Failures != 0 {
		t.Fatalf("expected violation should pass (exit %d): %+v", code, d)
	}

	// Offline check of the recorded history.
	code, _ = runCLI(t, "check", "--junit", j, filepath.Join(dir, "raft-stale-reads-seed2", "history.jsonl"))
	if d := readJUnit(t, j); code != exitViolation || d.Failures == 0 {
		t.Fatalf("check: exit %d, %+v", code, d)
	}

	// Live run (dry-run transport): keys plus the fault injection case.
	code, out = runCLI(t, "run", "--config", "../../testdata/memory.json", "--duration", "1s", "--dry-run", "--quiet", "--out", dir, "--junit", j)
	if code != exitOK {
		t.Fatalf("run: exit %d\n%s", code, out)
	}
	d = readJUnit(t, j)
	if d.Failures+d.Errors != 0 || d.Cases[len(d.Cases)-1].Name != "fault injection" {
		t.Fatalf("live JUnit: %+v", d)
	}

	md, _ := os.ReadFile(sum)
	for _, want := range []string{"### ❌ Strife · simulation · raft-stale-reads", "### ✅ Strife · simulation · raft-stale-reads", "Reproduce"} {
		if !strings.Contains(string(md), want) {
			t.Fatalf("summary lacks %q:\n%s", want, md)
		}
	}
}

func TestCIOutputsOnErrors(t *testing.T) {
	dir := t.TempDir()
	j := filepath.Join(dir, "j.xml")
	// Shrinking a run that does not fail is reported as an error case.
	code, _ := runCLI(t, "shrink", "--system", "raft", "--seed", "1", "--duration", "1s", "--out", "", "--junit", j)
	if d := readJUnit(t, j); code != exitUsage || d.Errors != 1 {
		t.Fatalf("exit %d, %+v", code, d)
	}
	// An unwritable JUnit path is an error, not a silent pass.
	code, out := runCLI(t, "sim", "--seed", "1", "--duration", "1s", "--out", "", "--junit", filepath.Join(dir, "missing", "dir", "j.xml"))
	if code != exitError || !strings.Contains(out, "writing CI outputs") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}
