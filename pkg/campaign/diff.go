package campaign

import (
	"sort"

	"github.com/kemalhafizh/strife/pkg/checker"
)

// ChangeKind classifies how a single seed's outcome changed relative to a baseline campaign.
type ChangeKind string

// Seed delta classifications.
const (
	ChangeRegressed         ChangeKind = "regressed"
	ChangeFixed             ChangeKind = "fixed"
	ChangePersistentFailure ChangeKind = "persistent_failure"
	ChangeNewFailure        ChangeKind = "new_failure"
	ChangeTraceDrift        ChangeKind = "trace_drift"
	ChangeUnchangedPass     ChangeKind = "unchanged_pass"
)

// SeedDelta records the transition of one seed between a baseline and current campaign.
type SeedDelta struct {
	Seed          uint64          `json:"seed"`
	Kind          ChangeKind      `json:"kind"`
	PrevVerdict   checker.Verdict `json:"prev_verdict,omitempty"`
	CurrVerdict   checker.Verdict `json:"curr_verdict"`
	PrevTraceHash string          `json:"prev_trace_hash,omitempty"`
	CurrTraceHash string          `json:"curr_trace_hash,omitempty"`
	Explanation   string          `json:"explanation,omitempty"`
	Reproduce     string          `json:"reproduce,omitempty"`
}

// Comparison summarizes all seed transitions between a baseline campaign and the current run.
type Comparison struct {
	BaselineCampaignID string      `json:"baseline_campaign_id,omitempty"`
	BaselineCommit     string      `json:"baseline_commit,omitempty"`
	BaselineTimestamp  string      `json:"baseline_timestamp,omitempty"`
	BaselineTotal      int         `json:"baseline_total"`
	Regressed          []SeedDelta `json:"regressed,omitempty"`
	Fixed              []SeedDelta `json:"fixed,omitempty"`
	PersistentFailures []SeedDelta `json:"persistent_failures,omitempty"`
	NewFailures        []SeedDelta `json:"new_failures,omitempty"`
	TraceDrifts        []SeedDelta `json:"trace_drifts,omitempty"`
	UnchangedPassCount int         `json:"unchanged_pass_count"`
}

// HasRegressions reports whether any seed that previously passed now fails or errors.
func (c *Comparison) HasRegressions() bool {
	return c != nil && len(c.Regressed) > 0
}

// Compare computes the seed-by-seed regression analysis between baseline and current.
func Compare(baseline, current *Manifest) *Comparison {
	if baseline == nil || current == nil {
		return nil
	}
	prevBySeed := make(map[uint64]SeedRecord, len(baseline.Seeds))
	for _, s := range baseline.Seeds {
		prevBySeed[s.Seed] = s
	}

	comp := &Comparison{
		BaselineCampaignID: baseline.CampaignID,
		BaselineCommit:     baseline.Commit,
		BaselineTimestamp:  baseline.Timestamp,
		BaselineTotal:      len(baseline.Seeds),
	}

	for _, curr := range current.Seeds {
		prev, existed := prevBySeed[curr.Seed]
		currOK := curr.Verdict == checker.Linearizable && curr.Error == ""
		if !existed {
			if !currOK {
				comp.NewFailures = append(comp.NewFailures, SeedDelta{
					Seed:          curr.Seed,
					Kind:          ChangeNewFailure,
					CurrVerdict:   curr.Verdict,
					CurrTraceHash: curr.TraceHash,
					Explanation:   curr.Explanation,
					Reproduce:     curr.Reproduce,
				})
			} else {
				comp.UnchangedPassCount++
			}
			continue
		}

		prevOK := prev.Verdict == checker.Linearizable && prev.Error == ""
		switch {
		case prevOK && !currOK:
			comp.Regressed = append(comp.Regressed, SeedDelta{
				Seed:          curr.Seed,
				Kind:          ChangeRegressed,
				PrevVerdict:   prev.Verdict,
				CurrVerdict:   curr.Verdict,
				PrevTraceHash: prev.TraceHash,
				CurrTraceHash: curr.TraceHash,
				Explanation:   curr.Explanation,
				Reproduce:     curr.Reproduce,
			})
		case !prevOK && currOK:
			comp.Fixed = append(comp.Fixed, SeedDelta{
				Seed:          curr.Seed,
				Kind:          ChangeFixed,
				PrevVerdict:   prev.Verdict,
				CurrVerdict:   curr.Verdict,
				PrevTraceHash: prev.TraceHash,
				CurrTraceHash: curr.TraceHash,
			})
		case !prevOK && !currOK:
			comp.PersistentFailures = append(comp.PersistentFailures, SeedDelta{
				Seed:          curr.Seed,
				Kind:          ChangePersistentFailure,
				PrevVerdict:   prev.Verdict,
				CurrVerdict:   curr.Verdict,
				PrevTraceHash: prev.TraceHash,
				CurrTraceHash: curr.TraceHash,
				Explanation:   curr.Explanation,
				Reproduce:     curr.Reproduce,
			})
		default:
			if prev.TraceHash != "" && curr.TraceHash != "" && prev.TraceHash != curr.TraceHash {
				comp.TraceDrifts = append(comp.TraceDrifts, SeedDelta{
					Seed:          curr.Seed,
					Kind:          ChangeTraceDrift,
					PrevVerdict:   prev.Verdict,
					CurrVerdict:   curr.Verdict,
					PrevTraceHash: prev.TraceHash,
					CurrTraceHash: curr.TraceHash,
				})
			} else {
				comp.UnchangedPassCount++
			}
		}
	}

	return comp
}

// MergeSeeds combines the requested seed list with any failing seeds from a
// baseline manifest (when replayFailing is true), returning a deduplicated,
// sorted slice of seeds.
func MergeSeeds(requested []uint64, baseline *Manifest, replayFailing bool) []uint64 {
	seen := make(map[uint64]struct{}, len(requested))
	var out []uint64
	for _, s := range requested {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	if replayFailing && baseline != nil {
		for _, rec := range baseline.Seeds {
			if rec.Verdict != checker.Linearizable || rec.Error != "" {
				if _, ok := seen[rec.Seed]; !ok {
					seen[rec.Seed] = struct{}{}
					out = append(out, rec.Seed)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
