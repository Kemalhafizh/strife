package compliance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/attest"
	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/report"
)

func TestEvaluateFullPassAndArtifacts(t *testing.T) {
	v := int64(1)
	ops := []history.Op{
		{Index: 0, Process: 0, Type: history.Invoke, F: history.FWrite, Key: "k0", Value: &v, Time: 100},
		{Index: 1, Process: 0, Type: history.OK, F: history.FWrite, Key: "k0", Value: &v, Time: 200},
	}
	sched := nemesis.Schedule{
		Nodes:    5,
		Duration: 10 * time.Second,
		Faults: []nemesis.Fault{
			{ID: 1, Kind: nemesis.Partition, Nodes: []int{0, 1}, Start: time.Second, End: 3 * time.Second},
			{ID: 2, Kind: nemesis.Kill, Nodes: []int{2}, Start: 2 * time.Second, End: 4 * time.Second},
			{ID: 3, Kind: nemesis.IOStall, Nodes: []int{3}, Start: 3 * time.Second, End: 5 * time.Second},
			{ID: 4, Kind: nemesis.ClockSkew, Nodes: []int{4}, Start: 4 * time.Second, End: 6 * time.Second},
		},
	}
	rep, err := report.New(
		report.KindSimulation,
		"raft",
		42,
		map[string]any{"nodes": 5},
		sched,
		ops,
		checker.Report{
			Model:      "kv",
			Verdict:    checker.Linearizable,
			Operations: 1,
			Checked:    1,
		},
		"trace-abc",
	)
	if err != nil {
		t.Fatal(err)
	}

	pub, priv, err := attest.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := attest.Sign(rep, priv, "chief-auditor")
	if err != nil {
		t.Fatal(err)
	}
	ver, err := attest.Verify(bundle, pub)
	if err != nil {
		t.Fatal(err)
	}

	dossier, err := Evaluate(Options{
		Report:       rep,
		Verification: ver,
		Frameworks:   AllFrameworks(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if dossier.Overall != StatusPass {
		t.Fatalf("expected overall status PASS, got %s (controls: %+v)", dossier.Overall, dossier.Controls)
	}
	if len(dossier.Controls) != 12 {
		t.Fatalf("expected 12 controls across 4 frameworks, got %d", len(dossier.Controls))
	}

	outDir := t.TempDir()
	written, err := WriteDir(outDir, dossier)
	if err != nil {
		t.Fatalf("WriteDir: %v", err)
	}
	if len(written) != 3 {
		t.Fatalf("expected 3 dossier files, got %d", len(written))
	}

	mdBytes, err := os.ReadFile(filepath.Join(outDir, "dossier.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dora.resilience-testing", "Art. 24", "PI1.3", "Annex A, 8.17", "bi-ojk.pemulihan-bencana", "POJK No. 11/POJK.03/2022", "chief-auditor"} {
		if !strings.Contains(string(mdBytes), want) {
			t.Errorf("dossier.md missing %q", want)
		}
	}

	htmlBytes, err := os.ReadFile(filepath.Join(outDir, "dossier.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(htmlBytes), "Regulatory Compliance &amp; Resilience Dossier") {
		t.Errorf("dossier.html missing header")
	}
}

func TestEvaluatePartialAndViolation(t *testing.T) {
	v := int64(1)
	ops := []history.Op{
		{Index: 0, Process: 0, Type: history.Invoke, F: history.FWrite, Key: "k0", Value: &v, Time: 100},
		{Index: 1, Process: 0, Type: history.OK, F: history.FWrite, Key: "k0", Value: &v, Time: 200},
	}
	// Only partition fault, unsigned -> should be StatusPartial when linearizable
	sched := nemesis.Schedule{
		Nodes:    3,
		Duration: 5 * time.Second,
		Faults: []nemesis.Fault{
			{ID: 1, Kind: nemesis.Partition, Nodes: []int{0}, Start: time.Second, End: 2 * time.Second},
		},
	}
	rep, _ := report.New(
		report.KindSimulation,
		"raft",
		1,
		nil,
		sched,
		ops,
		checker.Report{Model: "kv", Verdict: checker.Linearizable, Operations: 1},
		"trace-1",
	)

	dPartial, err := Evaluate(Options{Report: rep})
	if err != nil {
		t.Fatal(err)
	}
	if dPartial.Overall != StatusPartial {
		t.Fatalf("expected PARTIAL for unsigned partition-only run, got %s", dPartial.Overall)
	}

	// Violation -> should be StatusFail (recompute fingerprint so it is a genuine violating report, not a tampered one)
	rep.Verdict = checker.Violation
	rep.Check.Verdict = checker.Violation
	rep.Fingerprint = report.Fingerprint(rep)
	dFail, err := Evaluate(Options{Report: rep})
	if err != nil {
		t.Fatal(err)
	}
	if dFail.Overall != StatusFail {
		t.Fatalf("expected FAIL for violating report, got %s", dFail.Overall)
	}

	suite := Suite(dFail, true)
	if len(suite.Cases) != 12 {
		t.Fatalf("expected 12 JUnit cases, got %d", len(suite.Cases))
	}
}

func TestEvaluateRejectsTamperedReportOrMismatchedBundle(t *testing.T) {
	v := int64(1)
	ops := []history.Op{
		{Index: 0, Process: 0, Type: history.Invoke, F: history.FWrite, Key: "k0", Value: &v, Time: 100},
		{Index: 1, Process: 0, Type: history.OK, F: history.FWrite, Key: "k0", Value: &v, Time: 200},
	}
	repA, _ := report.New(
		report.KindSimulation,
		"raft-a",
		1,
		nil,
		nemesis.Schedule{Nodes: 3},
		ops,
		checker.Report{Model: "kv", Verdict: checker.Linearizable, Operations: 1},
		"trace-a",
	)
	repB, _ := report.New(
		report.KindSimulation,
		"raft-b",
		2,
		nil,
		nemesis.Schedule{Nodes: 3},
		ops,
		checker.Report{Model: "kv", Verdict: checker.Linearizable, Operations: 1},
		"trace-b",
	)

	pub, priv, _ := attest.GenerateKeyPair()
	bundleB, _ := attest.Sign(repB, priv, "auditor")
	verB, _ := attest.Verify(bundleB, pub)

	// 1. Supplying valid bundle for repB against repA must fail cross-binding check.
	if _, err := Evaluate(Options{Report: repA, Verification: verB}); err == nil {
		t.Fatalf("expected error when attaching bundle from a different report")
	}

	// 2. Tampering with repA's verdict without updating its fingerprint must fail integrity check.
	repA.Check.Verdict = checker.Violation
	if _, err := Evaluate(Options{Report: repA}); err == nil {
		t.Fatalf("expected error when report fingerprint does not match contents")
	}
}
