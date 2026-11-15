package campaign

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/explore"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/report"
)

func TestCompareRegressionsAndFixes(t *testing.T) {
	base := &Manifest{
		CampaignID: "raft-base",
		Commit:     "abc1111",
		Seeds: []SeedRecord{
			{Seed: 1, Verdict: checker.Linearizable, TraceHash: "h1"},
			{Seed: 2, Verdict: checker.Linearizable, TraceHash: "h2"},
			{Seed: 3, Verdict: checker.Violation, TraceHash: "h3", Explanation: "stale read"},
			{Seed: 4, Verdict: checker.Violation, TraceHash: "h4", Explanation: "stale read"},
			{Seed: 5, Verdict: checker.Linearizable, TraceHash: "h5-old"},
		},
	}

	curr := &Manifest{
		CampaignID: "raft-curr",
		Commit:     "def2222",
		Seeds: []SeedRecord{
			{Seed: 1, Verdict: checker.Linearizable, TraceHash: "h1"},
			{Seed: 2, Verdict: checker.Violation, TraceHash: "h2-bad", Explanation: "key k0: read 0 != 1"},
			{Seed: 3, Verdict: checker.Linearizable, TraceHash: "h3-fixed"},
			{Seed: 4, Verdict: checker.Violation, TraceHash: "h4", Explanation: "stale read"},
			{Seed: 5, Verdict: checker.Linearizable, TraceHash: "h5-new"},
			{Seed: 6, Verdict: checker.Violation, TraceHash: "h6", Explanation: "new seed bug"},
		},
	}

	comp := Compare(base, curr)
	if comp == nil {
		t.Fatal("expected non-nil Comparison")
	}
	if !comp.HasRegressions() {
		t.Fatal("expected HasRegressions() to be true")
	}
	if len(comp.Regressed) != 1 || comp.Regressed[0].Seed != 2 {
		t.Errorf("expected seed 2 in Regressed, got %+v", comp.Regressed)
	}
	if len(comp.Fixed) != 1 || comp.Fixed[0].Seed != 3 {
		t.Errorf("expected seed 3 in Fixed, got %+v", comp.Fixed)
	}
	if len(comp.PersistentFailures) != 1 || comp.PersistentFailures[0].Seed != 4 {
		t.Errorf("expected seed 4 in PersistentFailures, got %+v", comp.PersistentFailures)
	}
	if len(comp.TraceDrifts) != 1 || comp.TraceDrifts[0].Seed != 5 {
		t.Errorf("expected seed 5 in TraceDrifts, got %+v", comp.TraceDrifts)
	}
	if len(comp.NewFailures) != 1 || comp.NewFailures[0].Seed != 6 {
		t.Errorf("expected seed 6 in NewFailures, got %+v", comp.NewFailures)
	}
	if comp.UnchangedPassCount != 1 {
		t.Errorf("expected UnchangedPassCount=1, got %d", comp.UnchangedPassCount)
	}
}

func TestMergeSeeds(t *testing.T) {
	base := &Manifest{
		Seeds: []SeedRecord{
			{Seed: 1, Verdict: checker.Linearizable},
			{Seed: 42, Verdict: checker.Violation},
			{Seed: 99, Verdict: checker.Linearizable, Error: "timeout"},
		},
	}

	merged := MergeSeeds([]uint64{5, 2, 5, 1}, base, true)
	want := []uint64{1, 2, 5, 42, 99}
	if len(merged) != len(want) {
		t.Fatalf("expected %v, got %v", want, merged)
	}
	for i := range want {
		if merged[i] != want[i] {
			t.Fatalf("at %d: expected %d, got %d", i, want[i], merged[i])
		}
	}
}

func TestCampaignRunAndRegressionFlow(t *testing.T) {
	tmp := t.TempDir()
	baselinePath := filepath.Join(tmp, "baseline.json")
	outPass := filepath.Join(tmp, "out-pass")
	outRegress := filepath.Join(tmp, "out-regress")

	cfgPass := explore.DefaultConfig()
	cfgPass.System = "raft"
	cfgPass.Faults = []nemesis.Kind{nemesis.Partition}
	cfgPass.Duration = 3 * time.Second

	// 1. Run clean baseline campaign on correct Raft
	mPass, err := Run(context.Background(), Options{
		BaseConfig:       cfgPass,
		Seeds:            []uint64{1, 2, 3, 4, 5},
		Workers:          2,
		Commit:           "1111aaa",
		Branch:           "main",
		OutDir:           outPass,
		SaveBaselinePath: baselinePath,
		KeepReports:      2,
	})
	if err != nil {
		t.Fatalf("baseline campaign failed: %v", err)
	}
	if mPass.Passed != 5 || mPass.Violations != 0 {
		t.Fatalf("expected 5 passed seeds, got %+v", mPass)
	}
	if mPass.AvgAvailability <= 0 {
		t.Fatalf("expected positive AvgAvailability on campaign manifest, got %v", mPass.AvgAvailability)
	}
	if _, err := os.Stat(baselinePath); err != nil {
		t.Fatalf("expected baseline file to exist: %v", err)
	}

	// 2. Run buggy system against clean baseline to trigger regression detection
	cfgBug := cfgPass
	cfgBug.System = "raft-stale-reads"
	cfgBug.Duration = 10 * time.Second

	mReg, err := Run(context.Background(), Options{
		BaseConfig:    cfgBug,
		Seeds:         []uint64{1, 2, 3, 4, 5},
		Workers:       2,
		Commit:        "2222bbb",
		Branch:        "feature/stale-reads",
		OutDir:        outRegress,
		BaselinePath:  baselinePath,
		ReplayFailing: true,
		KeepReports:   1,
	})
	if err != nil {
		t.Fatalf("regression campaign failed: %v", err)
	}
	if mReg.Violations == 0 {
		t.Fatalf("expected at least one violation in raft-stale-reads")
	}
	if mReg.Comparison == nil || !mReg.Comparison.HasRegressions() {
		t.Fatalf("expected regressions against baseline, got %+v", mReg.Comparison)
	}

	// Verify HTML dashboard & embedded JSON
	htmlBytes, err := os.ReadFile(filepath.Join(outRegress, "campaign.html"))
	if err != nil {
		t.Fatalf("read campaign.html: %v", err)
	}
	htmlStr := string(htmlBytes)
	if !strings.Contains(htmlStr, "REGRESSED") {
		t.Errorf("expected campaign.html to contain REGRESSED badge")
	}
	const startTag = `<script id="campaign-data" type="application/json">`
	const endTag = `</script>`
	sIdx := strings.Index(htmlStr, startTag)
	if sIdx == -1 {
		t.Fatalf("missing campaign-data script tag in campaign.html")
	}
	rest := htmlStr[sIdx+len(startTag):]
	eIdx := strings.Index(rest, endTag)
	if eIdx == -1 {
		t.Fatalf("missing closing script tag in campaign.html")
	}
	var parsed Manifest
	if err := json.Unmarshal([]byte(rest[:eIdx]), &parsed); err != nil {
		t.Fatalf("embedded campaign-data JSON invalid: %v", err)
	}
	if parsed.CampaignID != mReg.CampaignID {
		t.Errorf("expected CampaignID %q, got %q", mReg.CampaignID, parsed.CampaignID)
	}

	// Verify CI Suite (JUnit + Summary)
	suite := Suite(mReg, "")
	var junitBuf, sumBuf bytes.Buffer
	if err := report.WriteJUnit(&junitBuf, []report.Suite{suite}); err != nil {
		t.Fatalf("WriteJUnit: %v", err)
	}
	if !strings.Contains(junitBuf.String(), "REGRESSION") {
		t.Errorf("expected JUnit XML to mention REGRESSION")
	}
	if err := report.WriteSummary(&sumBuf, []report.Suite{suite}); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}
	if !strings.Contains(sumBuf.String(), "regressed") {
		t.Errorf("expected Markdown summary to mention regressed count")
	}
}

func TestCampaignSettlementExplanation(t *testing.T) {
	cfg := explore.DefaultConfig()
	cfg.System = "outbox-dual-write"
	cfg.Faults = []nemesis.Kind{nemesis.Kill}
	cfg.Duration = 4 * time.Second

	m, err := Run(context.Background(), Options{
		BaseConfig: cfg,
		Seeds:      []uint64{1, 2, 3},
		Workers:    2,
	})
	if err != nil {
		t.Fatalf("campaign Run failed: %v", err)
	}
	if m.Violations == 0 {
		t.Fatalf("expected violation on outbox-dual-write campaign")
	}
	found := false
	for _, s := range m.Seeds {
		if strings.Contains(s.Explanation, "SETTLEMENT-ATOMICITY") || strings.Contains(s.Explanation, "SETTLEMENT-NO-ORPHAN-CREDIT") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected SETTLEMENT-* diagnosis in seed explanation, got %+v", m.Seeds)
	}
}

func TestCampaignSoundIncompleteAndNoAnomalyExpectation(t *testing.T) {
	base := &Manifest{
		CampaignID: "ledger-base",
		System:     "raft-ledger",
		TotalSeeds: 2,
		Unknown:    2,
		Seeds: []SeedRecord{
			{Seed: 1, Verdict: checker.Unknown, Reason: checker.ReasonIncomplete, TraceHash: "t1", Ops: 100},
			{Seed: 2, Verdict: checker.Unknown, Reason: checker.ReasonIncomplete, TraceHash: "t2", Ops: 100},
		},
	}
	if !base.IsIncomplete() {
		t.Fatal("expected IsIncomplete() = true for ReasonIncomplete seeds")
	}

	budget := &Manifest{
		CampaignID: "budget",
		System:     "raft",
		TotalSeeds: 1,
		Unknown:    1,
		Seeds:      []SeedRecord{{Seed: 1, Verdict: checker.Unknown, Reason: checker.ReasonSteps}},
	}
	if budget.IsIncomplete() {
		t.Fatal("expected IsIncomplete() = false for budget-exhausted Unknown seed")
	}

	regressedCurr := &Manifest{
		CampaignID: "ledger-curr",
		System:     "raft-ledger",
		TotalSeeds: 2,
		Violations: 1,
		Unknown:    1,
		Seeds: []SeedRecord{
			{Seed: 1, Verdict: checker.Unknown, Reason: checker.ReasonIncomplete, TraceHash: "t1"},
			{Seed: 2, Verdict: checker.Violation, TraceHash: "t2-bad", Explanation: "LEDGER-OVERDRAFT"},
		},
	}
	comp := Compare(base, regressedCurr)
	if comp == nil || !comp.HasRegressions() || len(comp.Regressed) != 1 || comp.Regressed[0].Seed != 2 {
		t.Fatalf("expected seed 2 regression from ReasonIncomplete to Violation, got %+v", comp)
	}
	if comp.UnchangedPassCount != 1 {
		t.Fatalf("expected UnchangedPassCount=1 for unchanged ReasonIncomplete seed, got %d", comp.UnchangedPassCount)
	}

	if merged := MergeSeeds([]uint64{1}, base, true); len(merged) != 1 {
		t.Fatalf("expected MergeSeeds not to treat ReasonIncomplete as failing, got %v", merged)
	}

	suitePass := Suite(base, "no-anomaly")
	for _, c := range suitePass.Cases {
		if c.Status != report.Passed {
			t.Errorf("no-anomaly Suite expected Passed case, got %s (%s)", c.Status, c.Message)
		}
	}
	suiteStrict := Suite(base, "")
	for _, c := range suiteStrict.Cases {
		if c.Status != report.Errored || !strings.Contains(c.Message, "sound incomplete") {
			t.Errorf("strict Suite expected Errored sound-incomplete case, got %s (%s)", c.Status, c.Message)
		}
	}
	suiteMixed := Suite(budget, "no-anomaly")
	if suiteMixed.Cases[0].Status != report.Errored {
		t.Errorf("no-anomaly Suite must still error on step-budget exhaustion, got %s", suiteMixed.Cases[0].Status)
	}

	var htmlBuf bytes.Buffer
	if err := WriteHTML(&htmlBuf, base); err != nil {
		t.Fatalf("WriteHTML: %v", err)
	}
	html := htmlBuf.String()
	if !strings.Contains(html, "INCOMPLETE") {
		t.Errorf("expected campaign HTML to render INCOMPLETE badge/tag")
	}
	if !strings.Contains(html, "Inconclusive") {
		t.Errorf("expected campaign HTML to render Inconclusive KPI and filter pill")
	}
}
