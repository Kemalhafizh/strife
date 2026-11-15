package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kemalhafizh/strife/pkg/campaign"
	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/report"
	"github.com/kemalhafizh/strife/pkg/serve"
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

func TestLiveShrinkAndScheduleReplay(t *testing.T) {
	dir := t.TempDir()
	cfg, err := os.ReadFile("../../testdata/memory.json")
	if err != nil {
		t.Fatal(err)
	}
	short := filepath.Join(dir, "short.json")
	if err := os.WriteFile(short, []byte(strings.Replace(string(cfg), `"10s"`, `"2s"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	// The in-memory store never violates: nothing to shrink.
	code, out := runCLI(t, "shrink", "--live", "--config", short, "--dry-run", "--attempts", "1", "--out", dir)
	if code != exitUsage || !strings.Contains(out, "did not reproduce") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	sched := filepath.Join(dir, "schedule.json")
	if err := os.WriteFile(sched, []byte(`{"nodes":0,"faults":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out = runCLI(t, "run", "--config", "../../testdata/memory.json", "--duration", "1s", "--dry-run", "--quiet", "--schedule", sched, "--out", dir)
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	logs, _ := filepath.Glob(filepath.Join(dir, "*", "commands.log"))
	if len(logs) != 1 {
		t.Fatalf("commands.log: %v", logs)
	}
	if data, _ := os.ReadFile(logs[0]); strings.Contains(string(data), "inject") {
		t.Fatalf("an empty schedule injected faults:\n%s", data)
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

func TestCampaignCLIAndRegression(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "baseline.json")
	outPass := filepath.Join(dir, "pass")
	outReg := filepath.Join(dir, "reg")
	jPath := filepath.Join(dir, "campaign.xml")

	// 1. Run baseline campaign on correct Raft
	code, out := runCLI(t, "campaign", "--system", "raft", "--faults", "partition", "--seeds", "1..4", "--duration", "3s", "--commit", "v1.0", "--out", outPass, "--save-baseline", basePath, "--expect", "linearizable")
	if code != exitOK {
		t.Fatalf("baseline campaign failed (exit %d):\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(outPass, "campaign.html")); err != nil {
		t.Fatalf("missing campaign.html: %v", err)
	}

	// 2. Run stale-reads against baseline -> must detect regression and fail --expect no-regression
	code, out = runCLI(t, "campaign", "--system", "raft-stale-reads", "--faults", "partition", "--seeds", "1..4", "--duration", "10s", "--commit", "v1.1-bug", "--out", outReg, "--baseline", basePath, "--expect", "no-regression", "--junit", jPath)
	if code != exitViolation {
		t.Fatalf("expected exit %d on regression, got %d:\n%s", exitViolation, code, out)
	}
	if !strings.Contains(out, "REGRESSED") {
		t.Fatalf("expected REGRESSED in output:\n%s", out)
	}
	d := readJUnit(t, jPath)
	if d.Tests != 4 || d.Failures == 0 {
		t.Fatalf("expected failing JUnit cases in campaign, got %+v", d)
	}
}

func TestCheckWithProbesCorrelation(t *testing.T) {
	dir := t.TempDir()
	histPath := filepath.Join(dir, "history.jsonl")
	probesPath := filepath.Join(dir, "probes.jsonl")
	outDir := filepath.Join(dir, "out")

	historyJSONL := strings.Join([]string{
		`{"id":0,"process":1,"type":"invoke","f":"write","key":"k0","value":1,"time":100000000,"node":"n1"}`,
		`{"id":1,"process":1,"type":"ok","f":"write","key":"k0","value":1,"time":120000000,"node":"n1"}`,
		`{"id":2,"process":2,"type":"invoke","f":"write","key":"k0","value":2,"time":200000000,"node":"n1"}`,
		`{"id":3,"process":2,"type":"ok","f":"write","key":"k0","value":2,"time":220000000,"node":"n1"}`,
		`{"id":4,"process":3,"type":"invoke","f":"read","key":"k0","time":300000000,"node":"n2"}`,
		`{"id":5,"process":3,"type":"ok","f":"read","key":"k0","value":1,"time":320000000,"node":"n2"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(histPath, []byte(historyJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	probesJSONL := `{"id":1,"at_ns":250000000,"duration_ns":95000000,"node":"n2","kind":"fsync_stall","process":"etcd","detail":"fsync(/var/lib/etcd/member/wal/0.wal) stalled for 95ms"}` + "\n"
	if err := os.WriteFile(probesPath, []byte(probesJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := runCLI(t, "check", "--history", histPath, "--probes", probesPath, "--out", outDir)
	if code != exitViolation {
		t.Fatalf("expected exit %d, got %d:\n%s", exitViolation, code, out)
	}
	if !strings.Contains(out, "root-cause [high]") || !strings.Contains(out, "fsync stalled for 95ms") {
		t.Fatalf("expected high-confidence root-cause attribution in output, got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(outDir, "probes.jsonl")); err != nil {
		t.Fatalf("expected probes.jsonl in report dir: %v", err)
	}
	mdBytes, err := os.ReadFile(filepath.Join(outDir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mdBytes), "Server-Side Probes & Root-Cause Attribution") {
		t.Fatalf("expected probe section in report.md:\n%s", string(mdBytes))
	}
	htmlBytes, err := os.ReadFile(filepath.Join(outDir, "report.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(htmlBytes), "Server-Side Probes &amp; Root-Cause Attribution") {
		t.Fatalf("expected probe section in report.html")
	}
}

func TestComplianceCLIAndDossier(t *testing.T) {
	dir := t.TempDir()
	simDir := filepath.Join(dir, "sim")
	planPath := filepath.Join(dir, "plan.json")
	keyPrefix := filepath.Join(dir, "auditor")
	bundlePath := filepath.Join(dir, "bundle.json")
	dossierDir := filepath.Join(dir, "dossier")
	jPath := filepath.Join(dir, "compliance-junit.xml")
	sumPath := filepath.Join(dir, "compliance-summary.md")

	planJSON := `{
  "seed": 3,
  "nodes": 5,
  "duration": 4000000000,
  "faults": [
    {"id": 1, "kind": "partition", "nodes": [0, 1], "start": 500000000, "end": 1200000000},
    {"id": 2, "kind": "kill", "nodes": [2], "start": 1300000000, "end": 2000000000},
    {"id": 3, "kind": "io-stall", "nodes": [3], "start": 2100000000, "end": 2800000000},
    {"id": 4, "kind": "clock-skew", "nodes": [4], "start": 2900000000, "end": 3500000000, "offset": 50000000}
  ]
}`
	if err := os.WriteFile(planPath, []byte(planJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := runCLI(t, "sim", "--system", "raft", "--seed", "3", "--duration", "4s", "--plan", planPath, "--out", simDir)
	if code != exitOK {
		t.Fatalf("sim failed (%d):\n%s", code, out)
	}
	repPath := filepath.Join(simDir, "raft-seed3", "report.json")

	code, out = runCLI(t, "keygen", "--out", keyPrefix)
	if code != exitOK {
		t.Fatalf("keygen failed (%d):\n%s", code, out)
	}

	code, out = runCLI(t, "sign", "--report", repPath, "--key", keyPrefix+".key", "--out", bundlePath, "--signer", "bank-auditor")
	if code != exitOK {
		t.Fatalf("sign failed (%d):\n%s", code, out)
	}

	code, out = runCLI(t, "compliance", "--report", repPath, "--bundle", bundlePath, "--key", keyPrefix+".pub", "--out", dossierDir, "--strict", "--junit", jPath, "--summary", sumPath)
	if code != exitOK {
		t.Fatalf("compliance failed (%d):\n%s", code, out)
	}
	if !strings.Contains(out, "overall=PASS") {
		t.Fatalf("expected overall=PASS in compliance output:\n%s", out)
	}
	for _, f := range []string{"dossier.json", "dossier.md", "dossier.html"} {
		if _, err := os.Stat(filepath.Join(dossierDir, f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
	d := readJUnit(t, jPath)
	if d.Tests != 18 || d.Failures != 0 || d.Errors != 0 {
		t.Fatalf("expected 18 passing JUnit control cases, got %+v", d)
	}
}

func TestWasmWorkloadCompileAndDeterminism(t *testing.T) {
	dir := t.TempDir()
	wasmPath := filepath.Join(dir, "cas-hotspot.wasm")
	simDir := filepath.Join(dir, "sim")

	code, out := runCLI(t, "workload", "--list")
	if code != exitOK || !strings.Contains(out, "preset:cas-hotspot") {
		t.Fatalf("workload --list failed (%d):\n%s", code, out)
	}

	code, out = runCLI(t, "workload", "--compile", "preset:cas-hotspot", "--out", wasmPath)
	if code != exitOK || !strings.Contains(out, "compiled workload plugin") {
		t.Fatalf("workload --compile failed (%d):\n%s", code, out)
	}

	code, out = runCLI(t, "workload", "--inspect", wasmPath, "--sample", "4")
	if code != exitOK || !strings.Contains(out, "source: wasm") {
		t.Fatalf("workload --inspect failed (%d):\n%s", code, out)
	}

	code, out = runCLI(t, "sim", "--system", "raft", "--seed", "7", "--duration", "2s", "--workload", wasmPath, "--determinism", "--out", simDir)
	if code != exitOK || !strings.Contains(out, "determinism: ✓ identical executions") {
		t.Fatalf("sim with wasm workload failed (%d):\n%s", code, out)
	}
}

func TestDoctorAndSettlementAndEDNCheck(t *testing.T) {
	dir := t.TempDir()

	// 1. strife doctor --json
	code, out := runCLI(t, "doctor", "--json")
	if code != exitOK || !strings.Contains(out, `"recommended_transport"`) {
		t.Fatalf("doctor --json failed (%d):\n%s", code, out)
	}

	// 2. outbox-settlement finishes with sound-incomplete no-anomaly (exitUnknown without --expect, exitOK with --expect no-anomaly), and outbox-dual-write finds a violation
	code, out = runCLI(t, "sim", "--system", "outbox-settlement", "--seed", "1", "--duration", "3s", "--out", "")
	if code != exitUnknown || !strings.Contains(out, "sound incomplete") {
		t.Fatalf("outbox-settlement without --expect should exit %d (sound incomplete), got (%d):\n%s", exitUnknown, code, out)
	}
	code, out = runCLI(t, "sim", "--system", "outbox-settlement", "--seed", "1", "--duration", "3s", "--expect", "no-anomaly", "--out", "")
	if code != exitOK {
		t.Fatalf("outbox-settlement --expect no-anomaly failed (%d):\n%s", code, out)
	}

	code, out = runCLI(t, "sim", "--system", "outbox-dual-write", "--seeds", "1..15", "--duration", "3s", "--expect", "violation", "--out", "")
	if code != exitOK {
		t.Fatalf("outbox-dual-write expected violation (%d):\n%s", code, out)
	}

	// 3. strife check on a Jepsen .edn history
	ednFile := filepath.Join(dir, "history.edn")
	ednData := `{:type :invoke, :f :write, :value [:k0 1], :time 100, :process 0}
{:type :ok,     :f :write, :value [:k0 1], :time 200, :process 0}
{:type :invoke, :f :read,  :value [:k0 nil], :time 300, :process 1}
{:type :ok,     :f :read,  :value [:k0 1], :time 400, :process 1}
`
	if err := os.WriteFile(ednFile, []byte(ednData), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out = runCLI(t, "check", "--history", ednFile, "--model", "kv")
	if code != exitOK || !strings.Contains(out, "verdict: LINEARIZABLE") {
		t.Fatalf("check on EDN history failed (%d):\n%s", code, out)
	}

	// 4. strife check --export-edn and --sarif
	exportedEDN := filepath.Join(dir, "exported.edn")
	sarifOut := filepath.Join(dir, "check.sarif")
	code, out = runCLI(t, "check", "--history", ednFile, "--model", "kv", "--export-edn", exportedEDN, "--sarif", sarifOut)
	if code != exitOK || !strings.Contains(out, "exported:") {
		t.Fatalf("check --export-edn --sarif failed (%d):\n%s", code, out)
	}
	if _, err := os.Stat(exportedEDN); err != nil {
		t.Fatalf("missing exported EDN file: %v", err)
	}
	if _, err := os.Stat(sarifOut); err != nil {
		t.Fatalf("missing SARIF file: %v", err)
	}

	// 5. strife doctor with unreachable endpoint config returns exitError
	cfgUnreachable := filepath.Join(dir, "down.json")
	cfgJSON := `{"name":"test-down","duration":"1s","nodes":[{"name":"n1","ip":"10.0.0.1"}],"transport":{"type":"proxy"},"db":{"kind":"postgres","endpoints":["127.0.0.1:1"]}}`
	if err := os.WriteFile(cfgUnreachable, []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out = runCLI(t, "doctor", "--config", cfgUnreachable)
	if code != exitError || !strings.Contains(out, "unreachable") {
		t.Fatalf("expected doctor with unreachable endpoint to exit with exitError (%d), got code %d:\n%s", exitError, code, out)
	}
}

func TestOTelCheckSLOGatesAndCIReport(t *testing.T) {
	dir := t.TempDir()

	// 1. strife check on an OpenTelemetry trace JSON file
	otelFile := filepath.Join(dir, "trace.otel.json")
	otelData := `{"resourceSpans":[{"scopeSpans":[{"spans":[
		{"name":"SET","startTimeUnixNano":"1000","endTimeUnixNano":"2000","attributes":[{"key":"db.key","value":{"stringValue":"k1"}},{"key":"db.value","value":{"intValue":"7"}}],"status":{"code":1}},
		{"name":"GET","startTimeUnixNano":"3000","endTimeUnixNano":"4000","attributes":[{"key":"db.key","value":{"stringValue":"k1"}},{"key":"db.result","value":{"intValue":"7"}}],"status":{"code":1}}
	]}]}]}`
	if err := os.WriteFile(otelFile, []byte(otelData), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := runCLI(t, "check", "--history", otelFile, "--model", "kv", "--min-availability", "95")
	if code != exitOK || !strings.Contains(out, "verdict: LINEARIZABLE") || !strings.Contains(out, "slo-gate: PASS") {
		t.Fatalf("check on OTel trace with SLO gate failed (%d):\n%s", code, out)
	}

	// 2. strife sim writing a report, followed by strife ci-report
	simDir := filepath.Join(dir, "sim-out")
	code, out = runCLI(t, "sim", "--system", "raft", "--seed", "1", "--duration", "2s", "--out", simDir)
	if code != exitOK {
		t.Fatalf("sim failed (%d):\n%s", code, out)
	}
	repFile := filepath.Join(simDir, "raft-seed1", "report.json")
	mdOut := filepath.Join(dir, "pr-comment.md")

	// Passing CI report
	code, out = runCLI(t, "ci-report", "--report", repFile, "--frameworks", "dora,bi-ojk", "--max-mttr", "10s", "--out", mdOut)
	if code != exitOK || !strings.Contains(out, "Strife CI Gate") || !strings.Contains(out, "PASS") {
		t.Fatalf("ci-report expected pass (%d):\n%s", code, out)
	}

	// Failing CI report when strict MTTR=1ns
	code, out = runCLI(t, "ci-report", "--report", repFile, "--max-mttr", "1ns")
	if code != exitViolation || !strings.Contains(out, "FAIL") {
		t.Fatalf("ci-report with 1ns max-mttr expected exitViolation (%d):\n%s", code, out)
	}
}

func startTestServe(t *testing.T, token string) (*serve.Server, string) {
	t.Helper()
	srv, err := serve.NewServer(serve.Config{
		ListenAddr: "127.0.0.1:0",
		StoreDir:   filepath.Join(t.TempDir(), "registry"),
		Token:      token,
	})
	if err != nil {
		t.Fatalf("serve.NewServer: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
		_ = ln.Close()
	})
	return srv, "http://" + ln.Addr().String()
}

func TestSimMultiSeedSLOGateAndPushRejection(t *testing.T) {
	dir := t.TempDir()
	jPath := filepath.Join(dir, "sim-slo.xml")

	code, out := runCLI(t, "sim", "--system", "raft", "--seeds", "1..3", "--duration", "4s", "--out", "", "--max-mttr", "1ns", "--junit", jPath)
	if code != exitViolation || !strings.Contains(out, "slo-gate: FAIL") {
		t.Fatalf("expected multi-seed sim --max-mttr 1ns to fail SLO gate (code=%d):\n%s", code, out)
	}
	d := readJUnit(t, jPath)
	foundSLO := false
	for _, c := range d.Cases {
		if c.Name == "resilience SLO gate" && c.Failure != nil {
			foundSLO = true
			break
		}
	}
	if !foundSLO {
		t.Fatalf("expected failing resilience SLO gate JUnit testcase, got %+v", d.Cases)
	}

	code, out = runCLI(t, "sim", "--system", "raft", "--seeds", "1..2", "--duration", "1s", "--out", "", "--push", "http://127.0.0.1:8080")
	if code != exitUsage || !strings.Contains(out, "--push cannot be combined with --seeds") {
		t.Fatalf("expected exitUsage when combining --push with --seeds, got code=%d:\n%s", code, out)
	}
}

func TestPushAcrossSimCampaignRunCheckAndWorkerCLI(t *testing.T) {
	srv, baseURL := startTestServe(t, "secret-push-tok")
	t.Setenv("STRIFE_TEST_PUSH_TOK", "secret-push-tok")
	dir := t.TempDir()

	// 1. sim single-seed push
	code, out := runCLI(t, "sim", "--system", "raft", "--seed", "1", "--duration", "2s", "--out", "", "--push", baseURL, "--push-token", "env:STRIFE_TEST_PUSH_TOK", "--tenant", "t1")
	if code != exitOK || !strings.Contains(out, "pushed:") {
		t.Fatalf("sim --push failed (code=%d):\n%s", code, out)
	}

	// 2. campaign push
	code, out = runCLI(t, "campaign", "--system", "raft", "--seeds", "1..2", "--duration", "2s", "--commit", "c123", "--branch", "main", "--out", "", "--push", baseURL, "--push-token", "env:STRIFE_TEST_PUSH_TOK", "--tenant", "t1")
	if code != exitOK || !strings.Contains(out, "pushed:") {
		t.Fatalf("campaign --push failed (code=%d):\n%s", code, out)
	}

	// 3. live dry-run push
	code, out = runCLI(t, "run", "--config", "../../testdata/memory.json", "--duration", "1s", "--dry-run", "--quiet", "--out", filepath.Join(dir, "live"), "--push", baseURL, "--push-token", "env:STRIFE_TEST_PUSH_TOK", "--tenant", "t1")
	if code != exitOK || !strings.Contains(out, "pushed:") {
		t.Fatalf("run --push failed (code=%d):\n%s", code, out)
	}

	// 4. check push + failing push preserves JUnit
	histPath := filepath.Join(dir, "hist.jsonl")
	histData := strings.Join([]string{
		`{"id":0,"process":1,"type":"invoke","f":"write","key":"k0","value":1,"time":1000000}`,
		`{"id":1,"process":1,"type":"ok","f":"write","key":"k0","value":1,"time":2000000}`,
		`{"id":2,"process":2,"type":"invoke","f":"read","key":"k0","time":3000000}`,
		`{"id":3,"process":2,"type":"ok","f":"read","key":"k0","value":1,"time":4000000}`,
	}, "\n") + "\n"
	if err := os.WriteFile(histPath, []byte(histData), 0o644); err != nil {
		t.Fatal(err)
	}
	jOK := filepath.Join(dir, "check-ok.xml")
	code, out = runCLI(t, "check", "--history", histPath, "--push", baseURL, "--push-token", "env:STRIFE_TEST_PUSH_TOK", "--tenant", "t1", "--junit", jOK)
	if code != exitOK || !strings.Contains(out, "pushed:") {
		t.Fatalf("check --push failed (code=%d):\n%s", code, out)
	}

	runs := srv.Store().List("", "", "t1")
	if len(runs) != 4 {
		t.Fatalf("expected 4 pushed artifacts for tenant t1, got %d", len(runs))
	}
	camps := srv.Store().List("", "campaign", "t1")
	if len(camps) != 1 || camps[0].Commit != "c123" || camps[0].Branch != "main" {
		t.Fatalf("expected pushed campaign metadata commit=c123 branch=main, got %+v", camps)
	}

	jFail := filepath.Join(dir, "check-fail-push.xml")
	code, out = runCLI(t, "check", "--history", histPath, "--push", baseURL, "--push-token", "wrong-tok", "--junit", jFail)
	if code != exitError || !strings.Contains(out, "push:") {
		t.Fatalf("expected exitError on bad --push-token, got code=%d:\n%s", code, out)
	}
	if d := readJUnit(t, jFail); d.Tests == 0 || d.Failures != 0 {
		t.Fatalf("expected JUnit report still written on push failure, got %+v", d)
	}

	// 5. Queue a job and run strife worker --tenant --once
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/jobs", strings.NewReader(`{"tenant":"pay","kind":"sim","system":"raft","seed":1,"duration":"1s"}`))
	req.Header.Set("Authorization", "Bearer secret-push-tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("enqueue status=%d", resp.StatusCode)
	}

	code, out = runCLI(t, "worker", "--server", baseURL, "--token", "env:STRIFE_TEST_PUSH_TOK", "--worker-id", "cli-w1", "--poll", "10ms", "--tenant", "pay", "--once")
	if code != exitOK || !strings.Contains(out, "processed 1 job(s)") {
		t.Fatalf("worker --once failed (code=%d):\n%s", code, out)
	}
	if payRuns := srv.Store().List("", "", "pay"); len(payRuns) != 1 {
		t.Fatalf("expected 1 completed run for tenant pay, got %d", len(payRuns))
	}
}

func TestCIReportModesAndDossierIntegrity(t *testing.T) {
	dir := t.TempDir()

	// Build candidate/baseline report, passing/failing campaigns, and compliance dossier.
	simDir := filepath.Join(dir, "sim")
	if code, out := runCLI(t, "sim", "--system", "raft", "--seed", "1", "--duration", "2s", "--out", simDir); code != exitOK {
		t.Fatalf("sim failed (%d):\n%s", code, out)
	}
	repFile := filepath.Join(simDir, "raft-seed1", "report.json")

	passCampDir := filepath.Join(dir, "camp-pass")
	if code, out := runCLI(t, "campaign", "--system", "raft", "--seeds", "1..2", "--duration", "2s", "--out", passCampDir); code != exitOK {
		t.Fatalf("passing campaign failed (%d):\n%s", code, out)
	}
	passCampFile := filepath.Join(passCampDir, "campaign.json")

	violCampDir := filepath.Join(dir, "camp-viol")
	if code, out := runCLI(t, "campaign", "--system", "raft-stale-reads", "--faults", "partition", "--seeds", "1..4", "--duration", "4s", "--out", violCampDir); code != exitViolation {
		t.Fatalf("violating campaign expected exitViolation (%d):\n%s", code, out)
	}
	violCampFile := filepath.Join(violCampDir, "campaign.json")

	dossierDir := filepath.Join(dir, "dossier")
	if code, out := runCLI(t, "compliance", "--report", repFile, "--frameworks", "dora,bi-ojk", "--out", dossierDir); code != exitOK {
		t.Fatalf("compliance failed (%d):\n%s", code, out)
	}
	dossierFile := filepath.Join(dossierDir, "dossier.json")

	// 1. --json - emits only JSON to stdout while still writing --out Markdown file.
	mdOut := filepath.Join(dir, "pr-digest.md")
	var outBuf, errBuf bytes.Buffer
	code := run([]string{"ci-report", "--report", repFile, "--baseline-report", repFile, "--out", mdOut, "--json", "-"}, &outBuf, &errBuf)
	if code != exitOK {
		t.Fatalf("ci-report --json - failed (%d): %s\n%s", code, errBuf.String(), outBuf.String())
	}
	var parsed ciReportJSON
	if err := json.Unmarshal(outBuf.Bytes(), &parsed); err != nil {
		t.Fatalf("expected stdout to be pure JSON with --json -, got parse error %v:\n%s", err, outBuf.String())
	}
	if !parsed.Passed || parsed.Target != "raft" {
		t.Fatalf("unexpected ci-report JSON payload: %+v", parsed)
	}
	if mdBytes, err := os.ReadFile(mdOut); err != nil || !strings.Contains(string(mdBytes), "## Strife CI Gate") {
		t.Fatalf("expected Markdown digest at %s, err=%v", mdOut, err)
	}

	// 2. --campaign + --baseline-campaign regression failure, plus standalone violating campaign.
	code, out := runCLI(t, "ci-report", "--campaign", violCampFile, "--baseline-campaign", passCampFile)
	if code != exitViolation || !strings.Contains(out, "Campaign Summary") || !strings.Contains(out, "Regressed seeds") {
		t.Fatalf("expected regression failure from ci-report (code=%d):\n%s", code, out)
	}
	code, _ = runCLI(t, "ci-report", "--campaign", violCampFile)
	if code != exitViolation {
		t.Fatalf("expected standalone violating campaign to fail ci-report, got %d", code)
	}

	// 3. --campaign + SLO gate without --report.
	code, out = runCLI(t, "ci-report", "--campaign", passCampFile, "--max-mttr", "1ns")
	if code != exitViolation || !strings.Contains(out, "Resilience SLO Gate") {
		t.Fatalf("expected campaign SLO gate failure (code=%d):\n%s", code, out)
	}

	// 4. --dossier verification + framework filter + --json file.
	jsonOut := filepath.Join(dir, "ci-verdict.json")
	code, out = runCLI(t, "ci-report", "--dossier", dossierFile, "--frameworks", "dora", "--json", jsonOut)
	if code != exitOK || !strings.Contains(out, "Regulatory Compliance") || !strings.Contains(out, "3 controls evaluated") {
		t.Fatalf("ci-report --dossier failed (code=%d):\n%s", code, out)
	}
	jb, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatal(err)
	}
	var dPayload ciReportJSON
	if err := json.Unmarshal(jb, &dPayload); err != nil || !dPayload.Passed || dPayload.DossierHash == "" || dPayload.CompliancePassed == nil || !*dPayload.CompliancePassed {
		t.Fatalf("unexpected dossier JSON output: %s (err=%v)", string(jb), err)
	}

	// 5. Tampered dossier rejected with exitViolation.
	origDossier, err := os.ReadFile(dossierFile)
	if err != nil {
		t.Fatal(err)
	}
	tamperedPath := filepath.Join(dir, "tampered-dossier.json")
	tamperedData := strings.Replace(string(origDossier), `"raft"`, `"tampered"`, 1)
	if err := os.WriteFile(tamperedPath, []byte(tamperedData), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out = runCLI(t, "ci-report", "--dossier", tamperedPath)
	if code != exitViolation || !strings.Contains(out, "integrity check failed") {
		t.Fatalf("expected exitViolation on tampered dossier (code=%d):\n%s", code, out)
	}

	// 6. Missing baseline file returns explicit error instead of being ignored.
	code, _ = runCLI(t, "ci-report", "--report", repFile, "--baseline-report", filepath.Join(dir, "missing.json"))
	if code != exitError {
		t.Fatalf("expected exitError on missing --baseline-report, got %d", code)
	}
}

func TestCLIUsageValidationAcrossSubcommands(t *testing.T) {
	cases := [][]string{
		{"sim", "--max-mttr", "-1s"},
		{"sim", "--min-availability", "101"},
		{"check", "--min-availability", "-0.5", "--history", "h.jsonl"},
		{"run", "--max-mttr", "-1ms", "--config", "../../testdata/memory.json"},
		{"compliance", "--min-availability", "150", "--report", "r.json"},
		{"serve", "--min-availability", "-1"},
		{"serve", "--job-lease", "0s"},
		{"serve", "--job-lease", "-5s"},
		{"serve", "--tenant-tokens", "malformed"},
		{"serve", "--tenant-tokens", "alpha="},
		{"serve", "--tenant-tokens", "alpha=tok1,alpha=tok2"},
		{"serve", "--tenant-tokens", "alpha=env:STRIFE_UNSET_TOKEN_ENV_VAR"},
		{"worker", "--poll", "-1s"},
		{"ci-report"},
		{"ci-report", "--report", "r.json", "--max-mttr", "-5ms"},
		{"ci-report", "--dossier", "d.json", "--baseline-report", "b.json"},
		{"sim", "--expect", "bogus"},
		{"check", "--expect", "bogus", "--history", "h.jsonl"},
		{"run", "--expect", "bogus", "--config", "../../testdata/memory.json"},
		{"campaign", "--expect", "bogus"},
		{"ci-report", "--expect", "bogus", "--report", "r.json"},
	}
	for _, args := range cases {
		if code, out := runCLI(t, args...); code != exitUsage {
			t.Errorf("%v: want exitUsage (%d), got %d:\n%s", args, exitUsage, code, out)
		}
	}
}

func TestServeCLITenantTokensAndJobLease(t *testing.T) {
	dir := t.TempDir()
	emptySecretFile := filepath.Join(dir, "empty.tok")
	if err := os.WriteFile(emptySecretFile, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := runCLI(t, "serve", "--tenant-tokens", "alpha=file:"+emptySecretFile); code != exitUsage || !strings.Contains(out, "empty resolved token") {
		t.Fatalf("expected exitUsage on empty resolved secret file, got code=%d:\n%s", code, out)
	}

	tokFile := filepath.Join(dir, "beta.tok")
	if err := os.WriteFile(tokFile, []byte("secret-beta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STRIFE_CLI_ADMIN_TOK", "secret-admin")
	t.Setenv("STRIFE_CLI_ALPHA_TOK", "secret-alpha")

	var errBuf bytes.Buffer
	cfg, code, err := parseServeConfig([]string{
		"--store", filepath.Join(dir, "registry"),
		"--token", "env:STRIFE_CLI_ADMIN_TOK",
		"--tenant-tokens", "alpha=env:STRIFE_CLI_ALPHA_TOK,beta=file:" + tokFile,
		"--job-lease", "15s",
	}, &errBuf)
	if err != nil || code != exitOK {
		t.Fatalf("parseServeConfig failed (code=%d, err=%v): %s", code, err, errBuf.String())
	}
	if cfg.Token != "secret-admin" {
		t.Fatalf("expected resolved admin token, got %q", cfg.Token)
	}
	if cfg.TenantTokens["alpha"] != "secret-alpha" || cfg.TenantTokens["beta"] != "secret-beta" {
		t.Fatalf("unexpected resolved tenant tokens: %+v", cfg.TenantTokens)
	}
	if cfg.JobLease.String() != "15s" {
		t.Fatalf("expected JobLease 15s, got %v", cfg.JobLease)
	}

	srv, err := serve.NewServer(cfg)
	if err != nil {
		t.Fatalf("serve.NewServer: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
		_ = ln.Close()
	})
	baseURL := "http://" + ln.Addr().String()

	// Unauthenticated GET /api/v1/runs must be rejected.
	resp, err := http.Get(baseURL + "/api/v1/runs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated GET /api/v1/runs, got %d", resp.StatusCode)
	}

	// Tenant alpha token cannot enqueue jobs for tenant beta.
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/jobs", strings.NewReader(`{"tenant":"beta","kind":"sim","system":"raft"}`))
	req.Header.Set("Authorization", "Bearer secret-alpha")
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-tenant POST /api/v1/jobs, got %d", resp.StatusCode)
	}
}

func TestSystemsAndNoAnomalyExpectation(t *testing.T) {
	code, out := runCLI(t, "systems")
	if code != exitOK {
		t.Fatalf("systems exit %d:\n%s", code, out)
	}
	for _, want := range []string{"raft", "raft-ledger", "no-anomaly", "2pc-ledger", "outbox-settlement"} {
		if !strings.Contains(out, want) {
			t.Fatalf("systems output missing %q:\n%s", want, out)
		}
	}

	// Sound-incomplete ledger systems must not pass --expect linearizable, and must pass --expect no-anomaly.
	code, out = runCLI(t, "sim", "--system", "raft-ledger", "--seeds", "1..2", "--duration", "2s", "--expect", "linearizable", "--out", "")
	if code != exitViolation {
		t.Fatalf("expected exitViolation for raft-ledger --expect linearizable, got %d:\n%s", code, out)
	}
	code, out = runCLI(t, "sim", "--system", "raft-ledger", "--seeds", "1..2", "--duration", "2s", "--expect", "no-anomaly", "--out", "")
	if code != exitOK {
		t.Fatalf("expected exitOK for raft-ledger --expect no-anomaly, got %d:\n%s", code, out)
	}

	// Campaign + ci-report --expect no-anomaly end-to-end.
	dir := t.TempDir()
	campDir := filepath.Join(dir, "camp")
	code, out = runCLI(t, "campaign", "--system", "outbox-settlement", "--seeds", "1..2", "--duration", "2s", "--expect", "no-anomaly", "--out", campDir)
	if code != exitOK || !strings.Contains(out, "sound incomplete") {
		t.Fatalf("campaign --expect no-anomaly failed (%d):\n%s", code, out)
	}
	campPath := filepath.Join(campDir, "campaign.json")
	code, out = runCLI(t, "ci-report", "--campaign", campPath)
	if code != exitViolation {
		t.Fatalf("ci-report without --expect on incomplete campaign should fail, got %d:\n%s", code, out)
	}
	code, out = runCLI(t, "ci-report", "--campaign", campPath, "--expect", "no-anomaly")
	if code != exitOK {
		t.Fatalf("ci-report --expect no-anomaly failed (%d):\n%s", code, out)
	}

	// Budget-exhausted campaign must still fail ci-report --expect no-anomaly.
	budgetCamp := &campaign.Manifest{
		Tool:       "strife",
		Version:    report.Version,
		CampaignID: "budget-1",
		System:     "raft",
		TotalSeeds: 1,
		Unknown:    1,
		Seeds: []campaign.SeedRecord{
			{Seed: 1, Verdict: checker.Unknown, Reason: checker.ReasonSteps},
		},
	}
	budgetCampPath := filepath.Join(dir, "budget-campaign.json")
	if err := campaign.WriteManifest(budgetCampPath, budgetCamp); err != nil {
		t.Fatal(err)
	}
	code, out = runCLI(t, "ci-report", "--campaign", budgetCampPath, "--expect", "no-anomaly")
	if code != exitViolation {
		t.Fatalf("ci-report --expect no-anomaly should reject step-budget unknown, got %d:\n%s", code, out)
	}

	// Large valid append history (>16 ops) -> Unknown (sound incomplete); passes --expect no-anomaly, fails --expect linearizable.
	histOps := make([]history.Op, 0, 40)
	var vals []int64
	for i := 1; i <= 20; i++ {
		if i%2 == 1 {
			v := int64(i)
			vals = append(vals, v)
			histOps = append(histOps,
				history.Op{Index: len(histOps), Process: 1, Type: history.Invoke, F: history.FTxn,
					Txn: []history.MicroOp{{F: history.MicroAppend, Key: "x", Value: history.Int(v)}}, Time: int64(i*2-1) * 1000},
				history.Op{Index: len(histOps) + 1, Process: 1, Type: history.OK, F: history.FTxn,
					Txn: []history.MicroOp{{F: history.MicroAppend, Key: "x", Value: history.Int(v)}}, Time: int64(i*2) * 1000},
			)
		} else {
			snap := append([]int64(nil), vals...)
			histOps = append(histOps,
				history.Op{Index: len(histOps), Process: 2, Type: history.Invoke, F: history.FTxn,
					Txn: []history.MicroOp{{F: history.MicroRead, Key: "x"}}, Time: int64(i*2-1) * 1000},
				history.Op{Index: len(histOps) + 1, Process: 2, Type: history.OK, F: history.FTxn,
					Txn: []history.MicroOp{{F: history.MicroRead, Key: "x", Values: snap}}, Time: int64(i*2) * 1000},
			)
		}
	}
	appendHistPath := filepath.Join(dir, "append-large.jsonl")
	af, err := os.Create(appendHistPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := history.WriteJSONL(af, histOps); err != nil {
		t.Fatal(err)
	}
	if err := af.Close(); err != nil {
		t.Fatal(err)
	}

	code, out = runCLI(t, "check", "--model", "append", "--history", appendHistPath)
	if code != exitUnknown || !strings.Contains(out, "sound incomplete") {
		t.Fatalf("large append check without --expect should exit %d (sound incomplete), got %d:\n%s", exitUnknown, code, out)
	}
	code, out = runCLI(t, "check", "--model", "append", "--expect", "linearizable", "--history", appendHistPath)
	if code != exitViolation {
		t.Fatalf("large append check --expect linearizable should exit %d, got %d:\n%s", exitViolation, code, out)
	}
	code, out = runCLI(t, "check", "--model", "append", "--expect", "no-anomaly", "--history", appendHistPath)
	if code != exitOK {
		t.Fatalf("large append check --expect no-anomaly should exit %d, got %d:\n%s", exitOK, code, out)
	}
}
