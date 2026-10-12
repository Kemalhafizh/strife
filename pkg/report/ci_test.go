package report

import (
	"bytes"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// parsed mirrors the subset of JUnit that CI systems read.
type parsed struct {
	Tests    int `xml:"tests,attr"`
	Failures int `xml:"failures,attr"`
	Errors   int `xml:"errors,attr"`
	Skipped  int `xml:"skipped,attr"`
	Suites   []struct {
		Name     string `xml:"name,attr"`
		Tests    int    `xml:"tests,attr"`
		Failures int    `xml:"failures,attr"`
		Props    []struct {
			Name  string `xml:"name,attr"`
			Value string `xml:"value,attr"`
		} `xml:"properties>property"`
		Cases []struct {
			Name    string `xml:"name,attr"`
			Class   string `xml:"classname,attr"`
			Time    string `xml:"time,attr"`
			Failure *struct {
				Message string `xml:"message,attr"`
				Type    string `xml:"type,attr"`
				Body    string `xml:",chardata"`
			} `xml:"failure"`
			Error *struct {
				Message string `xml:"message,attr"`
			} `xml:"error"`
			Skipped *struct{} `xml:"skipped"`
			Out     string    `xml:"system-out"`
		} `xml:"testcase"`
	} `xml:"testsuite"`
}

func parse(t *testing.T, b []byte) parsed {
	t.Helper()
	var p parsed
	if err := xml.Unmarshal(b, &p); err != nil {
		t.Fatalf("invalid JUnit XML: %v\n%s", err, b)
	}
	return p
}

func violatingReport() *Report {
	check := checker.Report{
		Model:   "kv",
		Verdict: checker.Violation,
		Keys: []checker.KeyResult{
			{Key: "k0", Verdict: checker.Linearizable, Operations: 10, Steps: 12},
			{Key: "k1", Verdict: checker.Violation, Operations: 9, Steps: 40, Counterexample: &checker.Counterexample{
				Explanation: `No linearization places "p2 read k1 → 1" before it returned. <&> | pipes`,
				Stuck:       checker.Step{Description: "p2 read k1 → 1", Call: 5, Return: 9},
				Linearized:  []checker.Step{{Description: "p0 write k1 2 (ok)", Call: 1, Return: 3}},
				StateAfter:  "2",
			}},
			{Key: "k2", Verdict: checker.Unknown, Operations: 400},
		},
	}
	r := &Report{Tool: "strife", Version: Version, Kind: KindSimulation, Target: "raft-x", Seed: 7, Verdict: checker.Violation,
		Fingerprint: "fp", HistoryHash: "hh", TraceHash: "th", ScheduleHash: "sh", Check: check,
		Schedule:  nemesis.Schedule{Faults: []nemesis.Fault{{ID: 0, Kind: nemesis.Partition}}},
		Reproduce: "strife sim --system raft-x --seed 7"}
	return r
}

func TestJUnitFromReport(t *testing.T) {
	var b bytes.Buffer
	s := SuiteFromReport(violatingReport(), 1500*time.Millisecond)
	if err := WriteJUnit(&b, []Suite{s}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.String(), "<?xml") {
		t.Fatal("missing XML declaration")
	}
	p := parse(t, b.Bytes())
	if p.Tests != 3 || p.Failures != 1 || p.Errors != 1 || len(p.Suites) != 1 {
		t.Fatalf("bad totals: %+v", p)
	}
	cs := p.Suites[0].Cases
	if cs[0].Name != "key k0" || cs[0].Class != "raft-x.seed7" || cs[0].Failure != nil {
		t.Fatalf("bad passing case: %+v", cs[0])
	}
	f := cs[1].Failure
	if f == nil || f.Type != "LinearizabilityViolation" || !strings.Contains(f.Message, "<&> | pipes") {
		t.Fatalf("bad failure: %+v", f)
	}
	for _, want := range []string{"Stuck: p2 read k1 → 1", "p0 write k1 2 (ok)", "Reproduce:\n  strife sim --system raft-x --seed 7"} {
		if !strings.Contains(f.Body, want) {
			t.Fatalf("failure body lacks %q:\n%s", want, f.Body)
		}
	}
	if cs[2].Error == nil || !strings.Contains(cs[2].Error.Message, "--max-steps") {
		t.Fatalf("unknown verdict must be an error: %+v", cs[2])
	}
	props := map[string]string{}
	for _, pr := range p.Suites[0].Props {
		props[pr.Name] = pr.Value
	}
	if props["fingerprint"] != "fp" || props["trace_sha256"] != "th" || props["seed"] != "7" || props["reproduce"] == "" {
		t.Fatalf("bad properties: %v", props)
	}
}

func TestLiveSuiteReportsInjectionErrors(t *testing.T) {
	r := violatingReport()
	r.Kind = KindLive
	r.Check.Keys = r.Check.Keys[:1]
	r.Applied = []Applied{
		{FaultID: 0, Kind: nemesis.Partition, Start: true},
		{FaultID: 0, Kind: nemesis.Partition, Start: false, Errors: []string{"n1: iptables: not found"}},
	}
	s := SuiteFromReport(r, time.Second)
	last := s.Cases[len(s.Cases)-1]
	if last.Name != "fault injection" || last.Status != Errored || !strings.Contains(last.Detail, "iptables: not found") {
		t.Fatalf("bad injection case: %+v", last)
	}
	r.Applied = r.Applied[:1]
	if c := SuiteFromReport(r, time.Second).Cases; c[len(c)-1].Status != Passed {
		t.Fatal("clean injection should pass")
	}
}

func seeds() []SeedResult {
	return []SeedResult{
		{Seed: 3, Verdict: checker.Linearizable, Ops: 100, Faults: 2},
		{Seed: 1, Verdict: checker.Violation, Ops: 90, Faults: 3, Explanation: "key k0: stale read", Reproduce: "strife sim --seed 1"},
		{Seed: 2, Verdict: checker.Unknown},
		{Seed: 4, Error: "boom"},
	}
}

func TestExplorationSuite(t *testing.T) {
	s := ExplorationSuite("raft-x", "partition", seeds(), false, time.Second)
	if len(s.Cases) != 4 || s.Cases[0].Name != "seed 1" {
		t.Fatalf("cases must be per seed, in seed order: %+v", s.Cases)
	}
	if s.Count(Failed) != 1 || s.Count(Errored) != 2 || s.Count(Passed) != 1 {
		t.Fatalf("bad counts: %+v", s.Cases)
	}
	if !strings.Contains(s.Cases[0].Detail, "strife sim --seed 1") || s.Reproduce != "strife sim --seed 1" {
		t.Fatalf("reproduce missing: %+v", s.Cases[0])
	}
}

func TestExplorationSuiteExpectingViolation(t *testing.T) {
	s := ExplorationSuite("raft-x", "partition", seeds(), true, time.Second)
	if len(s.Cases) != 1 || s.Cases[0].Status != Passed || !strings.Contains(s.Cases[0].Output, "[1]") {
		t.Fatalf("finding the bug must pass: %+v", s.Cases)
	}
	none := ExplorationSuite("raft-x", "partition", seeds()[:1], true, time.Second)
	if none.Cases[0].Status != Failed {
		t.Fatal("missing the bug must fail")
	}
}

func TestSummaryMarkdown(t *testing.T) {
	var b bytes.Buffer
	suites := []Suite{SuiteFromReport(violatingReport(), time.Second), ExplorationSuite("ok-sys", "none", seeds()[:1], false, time.Second)}
	if err := WriteSummary(&b, suites); err != nil {
		t.Fatal(err)
	}
	md := b.String()
	for _, want := range []string{"### ❌ Strife · simulation · raft-x", "### ✅ Strife · simulation · ok-sys",
		"| `key k1` | ❌ violation |", "| `key k2` | ⚠️ inconclusive |", `<&> \| pipes`, "strife sim --system raft-x --seed 7"} {
		if !strings.Contains(md, want) {
			t.Fatalf("summary lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "`key k0`") {
		t.Fatal("passing cases should not be listed")
	}
}

func TestOutputsAppendSummaryAndReplaceJUnit(t *testing.T) {
	dir := t.TempDir()
	o := Outputs{JUnit: filepath.Join(dir, "j.xml"), Summary: filepath.Join(dir, "s.md")}
	s := ErrorSuite("run", errors.New("cluster unreachable"))
	for i := 0; i < 2; i++ {
		if err := o.Write([]Suite{s}); err != nil {
			t.Fatal(err)
		}
	}
	md, _ := os.ReadFile(o.Summary)
	if strings.Count(string(md), "### ⚠️ Strife · run") != 2 {
		t.Fatalf("summary should be appended to:\n%s", md)
	}
	j, _ := os.ReadFile(o.JUnit)
	if p := parse(t, j); p.Errors != 1 || p.Tests != 1 {
		t.Fatalf("JUnit should be replaced, got %+v", p)
	}
	if (Outputs{}).Enabled() {
		t.Fatal("empty outputs are disabled")
	}
}
