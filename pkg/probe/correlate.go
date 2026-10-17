package probe

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// Finding correlates a consistency violation or indeterminate client operation
// with concurrent server-side diagnostic events and injected faults.
type Finding struct {
	Key         string   `json:"key,omitempty"`
	Operation   string   `json:"operation"`
	Confidence  string   `json:"confidence,omitempty"`
	ServerEvent Event    `json:"server_event,omitempty"`
	WindowStart int64    `json:"window_start"`
	WindowEnd   int64    `json:"window_end"`
	Events      []Event  `json:"events,omitempty"`
	Faults      []string `json:"faults,omitempty"`
	Summary     string   `json:"summary"`
	Hypothesis  string   `json:"hypothesis"`
}

const causalLookbackNs = int64(500 * time.Millisecond)

// Correlate analyzes a checked history, fault schedule, and server-side probe
// events to produce root-cause findings explaining violations or operation stalls.
func Correlate(ops []history.Op, check checker.Report, sched nemesis.Schedule, events []Event) []Finding {
	if len(events) == 0 && len(check.Failures()) == 0 {
		return nil
	}

	sortedEvents := append([]Event(nil), events...)
	sort.SliceStable(sortedEvents, func(i, j int) bool {
		if sortedEvents[i].Start != sortedEvents[j].Start {
			return sortedEvents[i].Start < sortedEvents[j].Start
		}
		return severityRank(sortedEvents[i].Kind) < severityRank(sortedEvents[j].Kind)
	})

	var findings []Finding

	// 1. Correlate every linearizability violation counterexample.
	for _, kr := range check.Failures() {
		ce := kr.Counterexample
		if ce == nil {
			continue
		}
		winStart := ce.Stuck.Call
		winEnd := ce.Stuck.Return
		for _, w := range ce.Witness {
			if w.Call < winStart {
				winStart = w.Call
			}
			if w.Return != history.Infinity && w.Return > winEnd {
				winEnd = w.Return
			}
		}
		if winEnd == history.Infinity || winEnd < winStart {
			winEnd = winStart + int64(time.Second)
		}

		searchStart := winStart - causalLookbackNs
		if searchStart < 0 {
			searchStart = 0
		}

		matchedEvents := overlappingEvents(sortedEvents, searchStart, winEnd)
		matchedFaults := overlappingFaults(sched, searchStart, winEnd)

		confidence := "low"
		var primary Event
		if len(matchedEvents) > 0 {
			primary = matchedEvents[0]
			if severityRank(primary.Kind) <= 5 {
				confidence = "high"
			} else {
				confidence = "medium"
			}
		} else if len(matchedFaults) > 0 {
			confidence = "medium"
		}

		hyp := buildViolationHypothesis(kr.Key, ce, winStart, winEnd, matchedEvents, matchedFaults)
		f := Finding{
			Key:         kr.Key,
			Operation:   ce.Stuck.Description,
			Confidence:  confidence,
			ServerEvent: primary,
			WindowStart: winStart,
			WindowEnd:   winEnd,
			Events:      matchedEvents,
			Faults:      matchedFaults,
			Summary:     hyp,
			Hypothesis:  hyp,
		}
		findings = append(findings, f)
	}

	if len(findings) > 0 || len(sortedEvents) == 0 {
		return findings
	}

	// 2. If there are no linearizability violations, correlate indeterminate (info)
	// client operations with overlapping server-side probe events.
	paired, err := history.Pair(ops)
	if err != nil {
		return nil
	}
	for _, p := range paired {
		if p.Status != history.Info {
			continue
		}
		winStart := p.Call
		winEnd := p.Return
		if winEnd == history.Infinity || winEnd <= winStart {
			winEnd = winStart + int64(time.Second)
		}
		searchStart := winStart - causalLookbackNs
		if searchStart < 0 {
			searchStart = 0
		}
		matchedEvents := overlappingEvents(sortedEvents, searchStart, winEnd)
		if len(matchedEvents) == 0 {
			continue
		}
		matchedFaults := overlappingFaults(sched, searchStart, winEnd)
		opDesc := fmt.Sprintf("p%d %s(%s) -> info", p.Process, p.F, p.Key)
		var evSummaries []string
		for _, ev := range matchedEvents {
			evSummaries = append(evSummaries, ev.Summary())
		}
		hyp := fmt.Sprintf("indeterminate operation %s [%s → %s] coincided with server-side probe telemetry: %s",
			opDesc, fmtNs(winStart), fmtNs(winEnd), strings.Join(evSummaries, "; "))
		findings = append(findings, Finding{
			Key:         p.Key,
			Operation:   opDesc,
			Confidence:  "medium",
			ServerEvent: matchedEvents[0],
			WindowStart: winStart,
			WindowEnd:   winEnd,
			Events:      matchedEvents,
			Faults:      matchedFaults,
			Summary:     hyp,
			Hypothesis:  hyp,
		})
		if len(findings) >= 5 {
			break
		}
	}

	return findings
}

func overlappingEvents(events []Event, startNs, endNs int64) []Event {
	var out []Event
	for _, ev := range events {
		evEnd := ev.End
		if evEnd < ev.Start {
			evEnd = ev.Start
		}
		if ev.Start <= endNs && evEnd >= startNs {
			out = append(out, ev)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		r1, r2 := severityRank(out[i].Kind), severityRank(out[j].Kind)
		if r1 != r2 {
			return r1 < r2
		}
		return out[i].Start < out[j].Start
	})
	return out
}

func overlappingFaults(sched nemesis.Schedule, startNs, endNs int64) []string {
	var out []string
	for _, f := range sched.Faults {
		fStart := int64(f.Start)
		fEnd := int64(f.End)
		if fStart <= endNs && fEnd >= startNs {
			out = append(out, fmt.Sprintf("#%d %s [%s → %s]", f.ID, f.Describe(sched.Nodes), fmtNs(fStart), fmtNs(fEnd)))
		}
	}
	return out
}

func buildViolationHypothesis(key string, ce *checker.Counterexample, winStart, winEnd int64, events []Event, faults []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "key %s violation (%s at [%s → %s]): ", key, ce.Summary(), fmtNs(winStart), fmtNs(winEnd))
	if len(events) > 0 {
		var parts []string
		limit := len(events)
		if limit > 4 {
			limit = 4
		}
		for i := 0; i < limit; i++ {
			parts = append(parts, events[i].Summary())
		}
		fmt.Fprintf(&b, "server-side root cause observed — %s", strings.Join(parts, "; "))
		if len(events) > limit {
			fmt.Fprintf(&b, " (+%d more probe events)", len(events)-limit)
		}
		if len(faults) > 0 {
			fmt.Fprintf(&b, " during fault %s", strings.Join(faults, ", "))
		}
		return b.String()
	}
	if len(faults) > 0 {
		fmt.Fprintf(&b, "occurred during active fault %s (no server-side probe anomalies recorded)", strings.Join(faults, ", "))
		return b.String()
	}
	fmt.Fprintf(&b, "observed on client history without overlapping fault or probe events")
	return b.String()
}

func severityRank(k EventKind) int {
	switch k {
	case KindFsyncError:
		return 1
	case KindFsyncStall:
		return 2
	case KindOOMKill:
		return 3
	case KindClockJump:
		return 4
	case KindProcessSignal:
		return 5
	case KindIOPressure:
		return 6
	case KindCPUPressure:
		return 7
	default:
		return 8
	}
}

func fmtNs(ns int64) string {
	if ns == history.Infinity {
		return "∞"
	}
	return fmt.Sprintf("%.3fs", float64(ns)/float64(time.Second))
}
