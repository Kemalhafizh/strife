package report

import (
	"fmt"
	"github.com/kemalhafizh/strife/internal/stats"
	"io"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
)

func fmtTime(ns int64) string {
	if ns == history.Infinity {
		return "∞"
	}
	return fmt.Sprintf("%.3fs", float64(ns)/1e9)
}

func badge(v checker.Verdict) string {
	switch v {
	case checker.Linearizable:
		return "✅ LINEARIZABLE"
	case checker.Violation:
		return "❌ VIOLATION"
	}
	return "⚠️ UNKNOWN (search budget exhausted)"
}

// Markdown renders a concise human readable report.
func Markdown(w io.Writer, r *Report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Strife report — %s\n\n", r.Target)
	fmt.Fprintf(&b, "**Verdict:** %s  \n", badge(r.Verdict))
	fmt.Fprintf(&b, "**Kind:** %s · **Seed:** `%d` · **Model:** %s\n\n", r.Kind, r.Seed, r.Check.Model)
	if r.Reproduce != "" {
		fmt.Fprintf(&b, "Reproduce:\n\n```sh\n%s\n```\n\n", r.Reproduce)
	}

	if sh := r.Shrink; sh != nil {
		b.WriteString("## Shrinking\n\n")
		fmt.Fprintf(&b, "Minimised from a failing run in %d simulations (%v).\n\n", sh.Runs, sh.Elapsed.Round(time.Millisecond))
		b.WriteString("| | Original | Minimal |\n|---|---|---|\n")
		fmt.Fprintf(&b, "| Faults | %d | **%d** |\n", sh.OriginalFaults, sh.MinimalFaults)
		fmt.Fprintf(&b, "| Duration | %v | %v |\n", sh.OriginalDuration, sh.MinimalDuration)
		fmt.Fprintf(&b, "| Clients | %d | %d |\n", sh.OriginalClients, sh.MinimalClients)
		fmt.Fprintf(&b, "| Operations | %d | %d |\n\n", sh.OriginalOps, sh.MinimalOps)
		for _, s := range sh.Steps {
			fmt.Fprintf(&b, "- %s\n", s)
		}
		if sh.BudgetExhausted {
			b.WriteString("\n> The run budget was exhausted: the scenario is smaller but may not be 1-minimal.\n")
		}
		b.WriteString("\n> Removing faults changes everything downstream of them, so the minimal scenario reproduces *a* violation of the same system, not necessarily the identical anomaly as the original run.\n\n")
	}

	b.WriteString("## Integrity\n\n| Artifact | SHA-256 |\n|---|---|\n")
	fmt.Fprintf(&b, "| Fingerprint | `%s` |\n", r.Fingerprint)
	if r.ScheduleHash != "" {
		fmt.Fprintf(&b, "| Fault schedule | `%s` |\n", r.ScheduleHash)
	}
	fmt.Fprintf(&b, "| History | `%s` |\n", r.HistoryHash)
	if r.TraceHash != "" {
		fmt.Fprintf(&b, "| Execution trace (%d events) | `%s` |\n", r.Events, r.TraceHash)
	}
	b.WriteString("\n")

	s := r.Check.Stats
	b.WriteString("## Workload\n\n")
	fmt.Fprintf(&b, "%d operations: %d ok, %d failed (definitely not applied), %d indeterminate. %d were relevant to the model.\n\n",
		s.Invocations, s.OK, s.Fail, s.Info, r.Check.Checked)

	if len(r.Schedule.Faults) > 0 {
		b.WriteString("## Faults\n\n| # | Start | End | Fault |\n|---|---|---|---|\n")
		for _, f := range r.Schedule.Faults {
			fmt.Fprintf(&b, "| %d | %v | %v | %s |\n", f.ID, f.Start.Round(time.Millisecond), f.End.Round(time.Millisecond), f.Describe(r.Schedule.Nodes))
		}
		b.WriteString("\n")
	}
	if t := InjectionTiming(r.Applied); t.Count > 0 {
		fmt.Fprintf(&b, "**Fault timing** (%d transitions via %s, from the planned instant until every node confirmed): p50 %v · p95 %v · max %v\n\n",
			t.Count, r.Applied[0].Method, stats.Round(t.P50), stats.Round(t.P95), stats.Round(t.Max))
	}
	if len(r.Applied) > 0 {
		var errs []string
		for _, a := range r.Applied {
			for _, e := range a.Errors {
				errs = append(errs, fmt.Sprintf("- fault %d (%s, start=%v): %s", a.FaultID, a.Kind, a.Start, e))
			}
		}
		if len(errs) > 0 {
			b.WriteString("### Fault injection errors\n\n" + strings.Join(errs, "\n") + "\n\n")
		}
	}

	b.WriteString("## Keys\n\n| Key | Ops | Verdict | Search steps |\n|---|---|---|---|\n")
	for _, k := range r.Check.Keys {
		fmt.Fprintf(&b, "| `%s` | %d | %s | %d |\n", k.Key, k.Operations, k.Verdict, k.Steps)
	}
	b.WriteString("\n")

	for _, k := range r.Check.Failures() {
		ce := k.Counterexample
		if ce == nil {
			continue
		}
		fmt.Fprintf(&b, "## Counterexample on key `%s`\n\n%s\n\n", k.Key, ce.Summary())
		if len(ce.Witness) > 0 {
			fmt.Fprintf(&b, "**Minimal witness:** these %d operations alone prove the violation:\n\n", len(ce.Witness))
			b.WriteString("| Operation | Invoked | Completed |\n|---|---|---|\n")
			for _, s := range ce.Witness {
				fmt.Fprintf(&b, "| `%s` | %s | %s |\n", s.Description, fmtTime(s.Call), fmtTime(s.Return))
			}
			b.WriteString("\n")
		}
		if ce.Diagnosis != "" {
			fmt.Fprintf(&b, "<details><summary>How the search got stuck</summary>\n\n%s\n\n</details>\n\n", ce.Explanation)
		}
		fmt.Fprintf(&b, "**Stuck operation:** `%s` invoked at %s, returned at %s (history index %d)\n\n",
			ce.Stuck.Description, fmtTime(ce.Stuck.Call), fmtTime(ce.Stuck.Return), ce.Stuck.Index)
		lin := ce.Linearized
		if len(lin) > 8 {
			fmt.Fprintf(&b, "Last 8 of %d linearized operations:\n\n", len(lin))
			lin = lin[len(lin)-8:]
		} else {
			b.WriteString("Linearized prefix:\n\n")
		}
		for _, s := range lin {
			fmt.Fprintf(&b, "1. `%s` [%s → %s]\n", s.Description, fmtTime(s.Call), fmtTime(s.Return))
		}
		fmt.Fprintf(&b, "\nModel state after the prefix: `%s`\n\n", ce.StateAfter)
		if len(ce.Concurrent) > 0 {
			b.WriteString("Concurrent candidates that could not be placed first:\n\n")
			for i, s := range ce.Concurrent {
				if i == 10 {
					fmt.Fprintf(&b, "- … and %d more\n", len(ce.Concurrent)-10)
					break
				}
				fmt.Fprintf(&b, "- `%s` [%s → %s]\n", s.Description, fmtTime(s.Call), fmtTime(s.Return))
			}
			b.WriteString("\n")
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "> %s\n\n", n)
	}
	if len(r.Findings) > 0 || len(r.Probes) > 0 {
		b.WriteString("## Server-Side Probes & Root-Cause Attribution\n\n")
		if len(r.Findings) > 0 {
			b.WriteString("| Key | Confidence | Server Event | Summary |\n|---|---|---|---|\n")
			for _, f := range r.Findings {
				key := f.Key
				if key == "" {
					key = "—"
				}
				fmt.Fprintf(&b, "| `%s` | **%s** | `%s` on `%s` | %s |\n",
					key, f.Confidence, f.ServerEvent.Kind, f.ServerEvent.Node, f.Summary)
			}
			b.WriteString("\n")
		}
		if len(r.Probes) > 0 {
			fmt.Fprintf(&b, "Collected %d server-side diagnostic probe event(s):\n\n", len(r.Probes))
			b.WriteString("| # | Time | Node | Kind | Summary |\n|---|---|---|---|---|\n")
			for i, ev := range r.Probes {
				if i == 25 {
					fmt.Fprintf(&b, "| … | … | … | … | +%d more events in `probes.jsonl` |\n", len(r.Probes)-25)
					break
				}
				fmt.Fprintf(&b, "| %d | %s | `%s` | `%s` | %s |\n",
					ev.ID, fmtTime(ev.AtNs), ev.Node, ev.Kind, ev.Summary())
			}
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "---\nGenerated by Strife %s\n", r.Version)
	_, err := io.WriteString(w, b.String())
	return err
}
