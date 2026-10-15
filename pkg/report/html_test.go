package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

func sampleTestOps() []history.Op {
	v1, v2, v3 := int64(1), int64(2), int64(3)
	return []history.Op{
		{Process: 0, Type: history.Invoke, F: history.FWrite, Key: "k1", Value: v2, Time: 1_000_000, Index: 0},
		{Process: 0, Type: history.Ok, F: history.FWrite, Key: "k1", Value: v2, Time: 3_000_000, Index: 1},
		{Process: 1, Type: history.Invoke, F: history.FWrite, Key: "k1", Value: v3, Time: 4_000_000, Index: 2},
		{Process: 1, Type: history.Ok, F: history.FWrite, Key: "k1", Value: v3, Time: 7_000_000, Index: 3},
		{Process: 2, Type: history.Invoke, F: history.FRead, Key: "k1", Time: 8_000_000, Index: 4},
		{Process: 2, Type: history.Ok, F: history.FRead, Key: "k1", Value: v1, Time: 12_000_000, Index: 5},
	}
}

func sampleViolatingReport() *Report {
	r := violatingReport()
	r.Schedule = nemesis.Schedule{
		Nodes: 3,
		Faults: []nemesis.Fault{
			{ID: 0, Kind: nemesis.Partition, Start: 2 * time.Millisecond, End: 6 * time.Millisecond},
		},
	}
	// Mark stuck operation as index 4
	r.Check.Keys[1].Counterexample = &checker.Counterexample{
		Summary:     "stale read: p2 read 1, expected 3",
		Explanation: "read k1 returned 1 instead of 3",
		Stuck:       checker.Step{Index: 4, Process: 2, Call: 8_000_000, Return: 12_000_000, Description: "p2 read k1 → 1"},
		Witness: []checker.Step{
			{Index: 2, Process: 1, Call: 4_000_000, Return: 7_000_000, Description: "p1 write k1 3 (ok)"},
			{Index: 4, Process: 2, Call: 8_000_000, Return: 12_000_000, Description: "p2 read k1 → 1"},
		},
	}
	return r
}

func TestHTMLGeneratesValidDocument(t *testing.T) {
	rep := sampleViolatingReport()
	ops := sampleTestOps()

	var buf bytes.Buffer
	if err := HTML(&buf, rep, ops); err != nil {
		t.Fatalf("HTML() returned error: %v", err)
	}

	html := buf.String()

	// Assert essential DOCTYPE and elements
	expectedSnippets := []string{
		"<!doctype html>",
		"<title>Strife report · raft-x</title>",
		"id=\"interactive-timeline-app\"",
		"<canvas id=\"tl-main-canvas\"",
		"<canvas id=\"tl-minimap-canvas\"",
		"<dialog id=\"tl-detail-dialog\"",
		"<script id=\"strife-timeline-data\" type=\"application/json\">",
		"<noscript>",
		"Timeline (Static Fallback)",
		"p2 read k1 → 1",
		"Partition",
	}

	for _, snip := range expectedSnippets {
		if !strings.Contains(html, snip) {
			t.Errorf("HTML output missing expected snippet: %q", snip)
		}
	}
}

func TestHTMLTimelineJSONPayload(t *testing.T) {
	rep := sampleViolatingReport()
	ops := sampleTestOps()

	var buf bytes.Buffer
	if err := HTML(&buf, rep, ops); err != nil {
		t.Fatalf("HTML() error: %v", err)
	}

	html := buf.String()

	// Extract the embedded JSON payload
	startTag := `<script id="strife-timeline-data" type="application/json">`
	endTag := `</script>`
	startIdx := strings.Index(html, startTag)
	if startIdx < 0 {
		t.Fatalf("missing JSON data script tag")
	}
	startIdx += len(startTag)
	endIdx := strings.Index(html[startIdx:], endTag)
	if endIdx < 0 {
		t.Fatalf("missing closing script tag for JSON payload")
	}

	rawJSON := html[startIdx : startIdx+endIdx]

	var payload timelinePayload
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		t.Fatalf("failed to unmarshal timeline payload JSON: %v\nJSON:\n%s", err, rawJSON)
	}

	if payload.Target != "raft-x" {
		t.Errorf("expected target 'raft-x', got %q", payload.Target)
	}
	if !payload.HasAnomaly {
		t.Errorf("expected HasAnomaly true")
	}
	if payload.StuckIndex != 4 {
		t.Errorf("expected StuckIndex 4, got %d", payload.StuckIndex)
	}
	if len(payload.Ops) == 0 {
		t.Fatalf("expected operations in payload, got 0")
	}

	// Verify marked roles
	var stuckFound, writeFound bool
	for _, o := range payload.Ops {
		if o.Index == 4 && o.Mark == "stuck" {
			stuckFound = true
		}
		if o.Index == 2 && (o.Mark == "write" || o.Mark == "witness") {
			writeFound = true
		}
	}

	if !stuckFound {
		t.Errorf("expected stuck operation marked with 'stuck'")
	}
	if !writeFound {
		t.Errorf("expected preceding write marked with 'write' or 'witness'")
	}

	// Verify faults in payload
	if len(payload.Faults) != 1 || payload.Faults[0].Kind != "partition" {
		t.Errorf("expected 1 partition fault, got: %+v", payload.Faults)
	}

	// Verify focus window bounds
	if payload.FocusStart > payload.FocusEnd {
		t.Errorf("invalid focus bounds: [%d, %d]", payload.FocusStart, payload.FocusEnd)
	}
}

func TestHTMLLinearizableReport(t *testing.T) {
	rep := &Report{
		Tool:        "strife",
		Version:     Version,
		Kind:        KindSimulation,
		Target:      "etcd",
		Seed:        42,
		Verdict:     checker.Linearizable,
		Fingerprint: "fp-clean",
		HistoryHash: "hh-clean",
		Check: checker.Report{
			Model:   "kv",
			Verdict: checker.Linearizable,
			Keys: []checker.KeyResult{
				{Key: "k0", Verdict: checker.Linearizable, Operations: 10},
			},
		},
	}

	v1 := int64(10)
	ops := []history.Op{
		{Process: 0, Type: history.Invoke, F: history.FWrite, Key: "k0", Value: v1, Time: 100, Index: 0},
		{Process: 0, Type: history.Ok, F: history.FWrite, Key: "k0", Value: v1, Time: 200, Index: 1},
		{Process: 1, Type: history.Invoke, F: history.FRead, Key: "k0", Time: 300, Index: 2},
		{Process: 1, Type: history.Ok, F: history.FRead, Key: "k0", Value: v1, Time: 400, Index: 3},
	}

	var buf bytes.Buffer
	if err := HTML(&buf, rep, ops); err != nil {
		t.Fatalf("HTML() error: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "LINEARIZABLE") {
		t.Errorf("expected LINEARIZABLE in output")
	}
	if strings.Contains(html, "id=\"tl-btn-focus-anomaly\"") {
		t.Errorf("linearizable report should not render focus anomaly button")
	}
}

func TestHTMLEmptyHistory(t *testing.T) {
	rep := &Report{
		Tool:        "strife",
		Version:     Version,
		Kind:        KindCheck,
		Target:      "empty",
		Verdict:     checker.Linearizable,
		Fingerprint: "fp-empty",
		Check:       checker.Report{Verdict: checker.Linearizable},
	}

	var buf bytes.Buffer
	if err := HTML(&buf, rep, []history.Op{}); err != nil {
		t.Fatalf("HTML() on empty history must not return error: %v", err)
	}

	if !strings.Contains(buf.String(), "<!doctype html>") {
		t.Errorf("expected valid HTML generated on empty history")
	}
}
