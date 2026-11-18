package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

func TestComputeResilienceMTTRAndAvailability(t *testing.T) {
	sched := nemesis.Schedule{
		Nodes:    3,
		Duration: 10 * time.Second,
		Faults: []nemesis.Fault{
			{ID: 1, Kind: nemesis.Partition, Start: 2 * time.Second, End: 5 * time.Second},
		},
	}

	ops := []history.Op{
		// Healthy op before fault [1.0s -> 1.1s] OK
		{Index: 0, Process: 0, Type: history.Invoke, F: history.FWrite, Key: "k0", Value: history.Int(1), Time: int64(1000 * time.Millisecond)},
		{Index: 1, Process: 0, Type: history.OK, F: history.FWrite, Key: "k0", Value: history.Int(1), Time: int64(1100 * time.Millisecond)},
		// Op during fault [3.0s -> 3.5s] Fail
		{Index: 2, Process: 1, Type: history.Invoke, F: history.FRead, Key: "k0", Time: int64(3000 * time.Millisecond)},
		{Index: 3, Process: 1, Type: history.Fail, F: history.FRead, Key: "k0", Time: int64(3500 * time.Millisecond)},
		// First op completing after fault heal at 5.0s: completes at 5.25s -> MTTR = 250ms
		{Index: 4, Process: 0, Type: history.Invoke, F: history.FRead, Key: "k0", Time: int64(5100 * time.Millisecond)},
		{Index: 5, Process: 0, Type: history.OK, F: history.FRead, Key: "k0", Value: history.Int(1), Time: int64(5250 * time.Millisecond)},
	}

	res := ComputeResilience(sched, nil, ops)
	if res == nil {
		t.Fatalf("expected non-nil ResilienceMetrics")
	}
	if res.HealthyWindowAvailability != 1.0 {
		t.Fatalf("expected 1.0 healthy window availability, got %v", res.HealthyWindowAvailability)
	}
	if res.FaultWindowAvailability != 0.0 {
		t.Fatalf("expected 0.0 fault window availability, got %v", res.FaultWindowAvailability)
	}
	if res.MTTR.Count != 1 || res.MTTR.P50 != 250*time.Millisecond {
		t.Fatalf("expected MTTR p50=250ms, got %+v", res.MTTR)
	}

	// 1. Passing SLO gate (max MTTR 500ms, min availability 60%)
	passSLO := EvaluateSLO(res, SLOGate{MaxMTTR: 500 * time.Millisecond, MinAvailability: 60})
	if !passSLO.Passed {
		t.Fatalf("expected SLO gate to pass, got violations: %v", passSLO.Violations)
	}

	// 2. Failing SLO gate (max MTTR 100ms, min availability 90%)
	failSLO := EvaluateSLO(res, SLOGate{MaxMTTR: 100 * time.Millisecond, MinAvailability: 90})
	if failSLO.Passed || len(failSLO.Violations) != 2 {
		t.Fatalf("expected 2 SLO violations, got passed=%v violations=%v", failSLO.Passed, failSLO.Violations)
	}

	// 3. WritePRDigest
	rep := &Report{
		Target:     "raft-kv",
		Seed:       42,
		Verdict:    checker.Linearizable,
		Resilience: res,
		Check:      checker.Report{Model: "kv", Verdict: checker.Linearizable, Stats: history.Stats{Invocations: 3, OK: 2, Fail: 1}},
	}
	var buf bytes.Buffer
	ok, err := WritePRDigest(&buf, PRDigestOptions{
		Report: rep,
		SLO:    failSLO,
	})
	if err != nil {
		t.Fatalf("WritePRDigest: %v", err)
	}
	if ok {
		t.Fatalf("expected WritePRDigest to return false when SLO fails")
	}
	if !strings.Contains(buf.String(), "Resilience SLO Gate") || !strings.Contains(buf.String(), "FAIL") {
		t.Fatalf("unexpected PR digest output:\n%s", buf.String())
	}
}

func TestWritePRDigestCampaignAndDossierModes(t *testing.T) {
	// Standalone campaign with violations must fail.
	var standalone bytes.Buffer
	ok, err := WritePRDigest(&standalone, PRDigestOptions{
		Campaign: &PRCampaignSummary{
			System:     "raft",
			TotalSeeds: 3,
			Passed:     2,
			Violations: 1,
		},
	})
	if err != nil {
		t.Fatalf("WritePRDigest standalone: %v", err)
	}
	if ok || !strings.Contains(standalone.String(), "FAIL") {
		t.Fatalf("expected standalone violating campaign to fail, got ok=%v\n%s", ok, standalone.String())
	}

	// Baseline campaign with persistent violation but zero regressions/new failures passes no-regression gate.
	var baselineOK bytes.Buffer
	ok, err = WritePRDigest(&baselineOK, PRDigestOptions{
		Campaign: &PRCampaignSummary{
			System:      "raft",
			TotalSeeds:  3,
			Passed:      2,
			Violations:  1,
			HasBaseline: true,
		},
	})
	if err != nil {
		t.Fatalf("WritePRDigest baseline: %v", err)
	}
	if !ok || !strings.Contains(baselineOK.String(), "PASS") {
		t.Fatalf("expected baseline campaign without regressions to pass, got ok=%v\n%s", ok, baselineOK.String())
	}

	// Reportless digest with SLO + compliance renders check table.
	compPass := false
	var reportless bytes.Buffer
	ok, err = WritePRDigest(&reportless, PRDigestOptions{
		Target:         "ledger",
		SLO:            &SLOResult{Passed: true, MinAvailability: 0.99},
		CompliancePass: &compPass,
		ComplianceNote: "1 control failed",
	})
	if err != nil {
		t.Fatalf("WritePRDigest reportless: %v", err)
	}
	if ok {
		t.Fatalf("expected reportless digest with failing compliance to return false")
	}
	for _, want := range []string{"`ledger`", "| Resilience SLO Gate | ✅ PASS |", "| Regulatory Compliance | ❌ FAIL |"} {
		if !strings.Contains(reportless.String(), want) {
			t.Fatalf("reportless PR digest missing %q:\n%s", want, reportless.String())
		}
	}

	// Sound-incomplete report and campaign pass under Expect="no-anomaly" and fail otherwise.
	incReport := &Report{
		Target:  "raft-ledger",
		Seed:    1,
		Verdict: checker.Unknown,
		Check: checker.Report{
			Model:   "ledger",
			Verdict: checker.Unknown,
			Keys:    []checker.KeyResult{{Key: "ledger", Verdict: checker.Unknown, Reason: checker.ReasonIncomplete}},
		},
	}
	incCamp := &PRCampaignSummary{
		System:     "raft-ledger",
		TotalSeeds: 2,
		Unknown:    2,
		Incomplete: true,
	}
	var incPass bytes.Buffer
	ok, err = WritePRDigest(&incPass, PRDigestOptions{
		Expect:   "no-anomaly",
		Report:   incReport,
		Campaign: incCamp,
	})
	if err != nil {
		t.Fatalf("WritePRDigest no-anomaly: %v", err)
	}
	if !ok || !strings.Contains(incPass.String(), "PASS") || !strings.Contains(incPass.String(), "UNKNOWN (no anomaly found; incomplete)") {
		t.Fatalf("expected no-anomaly incomplete digest to pass, got ok=%v\n%s", ok, incPass.String())
	}

	var incStrict bytes.Buffer
	ok, err = WritePRDigest(&incStrict, PRDigestOptions{
		Expect:   "linearizable",
		Report:   incReport,
		Campaign: incCamp,
	})
	if err != nil {
		t.Fatalf("WritePRDigest strict: %v", err)
	}
	if ok {
		t.Fatalf("expected linearizable digest on incomplete run to fail:\n%s", incStrict.String())
	}

	budgetReport := &Report{
		Target:  "raft",
		Seed:    1,
		Verdict: checker.Unknown,
		Check: checker.Report{
			Model:   "kv",
			Verdict: checker.Unknown,
			Keys:    []checker.KeyResult{{Key: "k0", Verdict: checker.Unknown, Reason: checker.ReasonSteps}},
		},
	}
	var budgetOut bytes.Buffer
	ok, err = WritePRDigest(&budgetOut, PRDigestOptions{
		Expect: "no-anomaly",
		Report: budgetReport,
	})
	if err != nil {
		t.Fatalf("WritePRDigest budget: %v", err)
	}
	if ok {
		t.Fatalf("no-anomaly must still fail when report hit step budget:\n%s", budgetOut.String())
	}
}

func TestValidateVerdictAndPRDigestBaselineExpect(t *testing.T) {
	// ValidateVerdict backfills empty r.Verdict from Check.Verdict.
	backfill := &Report{
		Target: "raft",
		Check:  checker.Report{Verdict: checker.Linearizable},
	}
	if err := ValidateVerdict(backfill); err != nil || backfill.Verdict != checker.Linearizable {
		t.Fatalf("expected Verdict backfill, got err=%v verdict=%q", err, backfill.Verdict)
	}

	// ValidateVerdict rejects desync between Verdict and Check.Verdict.
	desync := &Report{
		Target:  "raft",
		Verdict: checker.Linearizable,
		Check:   checker.Report{Verdict: checker.Violation},
	}
	if err := ValidateVerdict(desync); err == nil {
		t.Fatal("expected ValidateVerdict to reject Verdict vs Check.Verdict mismatch")
	}
	var out bytes.Buffer
	if _, err := WritePRDigest(&out, PRDigestOptions{Report: desync}); err == nil {
		t.Fatal("expected WritePRDigest to reject Verdict mismatch")
	}

	// WritePRDigest must enforce explicit Expect even when HasBaseline is true.
	incompleteWithBaseline := &PRCampaignSummary{
		System:      "raft-ledger",
		HasBaseline: true,
		TotalSeeds:  2,
		Unknown:     2,
		Incomplete:  true,
	}
	var strictBuf bytes.Buffer
	ok, err := WritePRDigest(&strictBuf, PRDigestOptions{
		Expect:   "linearizable",
		Campaign: incompleteWithBaseline,
	})
	if err != nil {
		t.Fatalf("WritePRDigest linearizable with baseline: %v", err)
	}
	if ok {
		t.Fatalf("expected Expect=linearizable to fail on incomplete campaign even with HasBaseline:\n%s", strictBuf.String())
	}

	violWithBaseline := &PRCampaignSummary{
		System:      "raft",
		HasBaseline: true,
		TotalSeeds:  2,
		Passed:      1,
		Violations:  1,
	}
	var noAnomBuf bytes.Buffer
	ok, err = WritePRDigest(&noAnomBuf, PRDigestOptions{
		Expect:   "no-anomaly",
		Campaign: violWithBaseline,
	})
	if err != nil {
		t.Fatalf("WritePRDigest no-anomaly with baseline: %v", err)
	}
	if ok {
		t.Fatalf("expected Expect=no-anomaly to fail when campaign has persistent violations despite HasBaseline:\n%s", noAnomBuf.String())
	}
}
