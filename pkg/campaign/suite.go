package campaign

import (
	"fmt"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/report"
)

// Suite converts a campaign Manifest into a CI report.Suite suitable for
// JUnit XML and GitHub Step Summary output.
func Suite(m *Manifest, expect string) report.Suite {
	faults := make([]string, len(m.Faults))
	for i, f := range m.Faults {
		faults[i] = string(f)
	}
	faultStr := strings.Join(faults, ",")
	if faultStr == "" {
		faultStr = "none"
	}

	props := []report.Property{
		{Name: "strife.version", Value: m.Version},
		{Name: "campaign.id", Value: m.CampaignID},
		{Name: "fingerprint", Value: m.Fingerprint},
		{Name: "system", Value: m.System},
		{Name: "faults", Value: faultStr},
		{Name: "seeds", Value: fmt.Sprint(m.TotalSeeds)},
		{Name: "total_events", Value: fmt.Sprint(m.TotalEvents)},
		{Name: "total_ops", Value: fmt.Sprint(m.TotalOps)},
	}
	if m.Commit != "" {
		props = append(props, report.Property{Name: "commit", Value: m.Commit})
	}
	if m.Branch != "" {
		props = append(props, report.Property{Name: "branch", Value: m.Branch})
	}
	if m.Comparison != nil {
		if m.Comparison.BaselineCommit != "" {
			props = append(props, report.Property{Name: "baseline.commit", Value: m.Comparison.BaselineCommit})
		}
		props = append(props,
			report.Property{Name: "regressed", Value: fmt.Sprint(len(m.Comparison.Regressed))},
			report.Property{Name: "fixed", Value: fmt.Sprint(len(m.Comparison.Fixed))},
			report.Property{Name: "persistent_failures", Value: fmt.Sprint(len(m.Comparison.PersistentFailures))},
			report.Property{Name: "trace_drifts", Value: fmt.Sprint(len(m.Comparison.TraceDrifts))},
		)
	}

	headline := fmt.Sprintf("Campaign `%s` · %d seeds of `%s` under `%s`: **%d passed**, **%d violating**",
		m.CampaignID, m.TotalSeeds, m.System, faultStr, m.Passed, m.Violations)
	if m.Comparison != nil {
		headline += fmt.Sprintf(" · baseline diff: **%d regressed**, **%d fixed**, **%d persistent**",
			len(m.Comparison.Regressed), len(m.Comparison.Fixed), len(m.Comparison.PersistentFailures))
	}

	var firstRepro string
	if m.Comparison != nil && len(m.Comparison.Regressed) > 0 {
		firstRepro = m.Comparison.Regressed[0].Reproduce
	}
	if firstRepro == "" {
		for _, s := range m.Seeds {
			if s.Verdict == checker.Violation && s.Reproduce != "" {
				firstRepro = s.Reproduce
				break
			}
		}
	}

	elapsed := time.Duration(m.DurationMs) * time.Millisecond
	s := report.Suite{
		Name:       fmt.Sprintf("campaign · %s", m.System),
		Time:       elapsed,
		Properties: props,
		Headline:   headline,
		Reproduce:  firstRepro,
	}

	if expect == string(checker.Violation) {
		c := report.Case{
			Name:  "finds a violation",
			Class: m.System,
			Time:  elapsed,
		}
		if m.Violations > 0 {
			c.Status = report.Passed
			c.Output = fmt.Sprintf("%d violating seeds found\nreproduce: %s", m.Violations, firstRepro)
		} else {
			c.Status = report.Failed
			c.Message = fmt.Sprintf("expected a violation in %d seeds, found none", m.TotalSeeds)
		}
		s.Cases = []report.Case{c}
		return s
	}

	deltaBySeed := make(map[uint64]ChangeKind)
	if m.Comparison != nil {
		for _, d := range m.Comparison.Regressed {
			deltaBySeed[d.Seed] = ChangeRegressed
		}
		for _, d := range m.Comparison.Fixed {
			deltaBySeed[d.Seed] = ChangeFixed
		}
		for _, d := range m.Comparison.PersistentFailures {
			deltaBySeed[d.Seed] = ChangePersistentFailure
		}
		for _, d := range m.Comparison.NewFailures {
			deltaBySeed[d.Seed] = ChangeNewFailure
		}
		for _, d := range m.Comparison.TraceDrifts {
			deltaBySeed[d.Seed] = ChangeTraceDrift
		}
	}

	for _, rec := range m.Seeds {
		delta := deltaBySeed[rec.Seed]
		c := report.Case{
			Name:   fmt.Sprintf("seed %d", rec.Seed),
			Class:  m.System,
			Time:   time.Duration(rec.ElapsedMs) * time.Millisecond,
			Status: report.Passed,
			Output: fmt.Sprintf("%d operations, %d faults, %d events (trace %s)",
				rec.Ops, rec.Faults, rec.Events, shortHash(rec.TraceHash)),
		}
		switch {
		case rec.Error != "":
			c.Status = report.Errored
			c.Message = rec.Error
		case rec.Verdict == checker.Violation:
			if expect == "no-regression" && delta == ChangePersistentFailure {
				c.Status = report.Skipped
				c.Message = "known failure in baseline: " + rec.Explanation
			} else {
				c.Status = report.Failed
				msg := rec.Explanation
				if msg == "" {
					msg = "not linearizable"
				}
				if delta == ChangeRegressed {
					msg = "REGRESSION (passed in baseline → violation): " + msg
				}
				c.Message = msg
				c.Detail = fmt.Sprintf("%s\n\nReproduce:\n  %s\n", msg, rec.Reproduce)
			}
		case rec.Verdict == checker.Unknown:
			c.Status = report.Errored
			c.Message = "search budget exhausted before a verdict"
		}
		s.Cases = append(s.Cases, c)
	}
	return s
}
