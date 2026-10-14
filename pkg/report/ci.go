package report

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
)

// This file renders Strife outcomes for CI systems: JUnit XML (understood by
// GitHub, GitLab, Jenkins, Buildkite, CircleCI, Azure DevOps...) and a
// Markdown digest suitable for $GITHUB_STEP_SUMMARY. Both are produced from
// the same Suite model, so they never disagree.

// Status is the outcome of one CI test case.
type Status string

// Test case outcomes.
const (
	Passed  Status = "passed"
	Failed  Status = "failed"  // a consistency violation was found
	Errored Status = "errored" // the check could not reach a verdict
	Skipped Status = "skipped"
)

// Case is one CI test case: a key of a history, a seed of an exploration...
type Case struct {
	Name    string
	Class   string
	Time    time.Duration
	Status  Status
	Message string // one line
	Detail  string // counterexample, reproduction command
	Output  string // extra context (system-out)
}

// Property is a key/value pair attached to a suite.
type Property struct {
	Name  string
	Value string
}

// Suite groups cases.
type Suite struct {
	Name       string
	Time       time.Duration
	Properties []Property
	Cases      []Case
	// Headline is a one line human summary used by the Markdown digest.
	Headline string
	// Reproduce is shown prominently in the Markdown digest when set.
	Reproduce string
}

// Count returns how many cases have status s.
func (s Suite) Count(st Status) int {
	n := 0
	for _, c := range s.Cases {
		if c.Status == st {
			n++
		}
	}
	return n
}

// OK reports whether the suite has neither failures nor errors.
func (s Suite) OK() bool { return s.Count(Failed) == 0 && s.Count(Errored) == 0 }

// ---------------------------------------------------------------------------
// JUnit XML.

type junitSuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Errors   int          `xml:"errors,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Time     string       `xml:"time,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name       string          `xml:"name,attr"`
	Tests      int             `xml:"tests,attr"`
	Failures   int             `xml:"failures,attr"`
	Errors     int             `xml:"errors,attr"`
	Skipped    int             `xml:"skipped,attr"`
	Time       string          `xml:"time,attr"`
	Properties *junitProps     `xml:"properties,omitempty"`
	Cases      []junitTestCase `xml:"testcase"`
}

type junitProps struct {
	Props []junitProp `xml:"property"`
}

type junitProp struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type junitTestCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitProblem `xml:"failure,omitempty"`
	Error     *junitProblem `xml:"error,omitempty"`
	Skipped   *junitSkipped `xml:"skipped,omitempty"`
	SystemOut string        `xml:"system-out,omitempty"`
}

type junitProblem struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

type junitSkipped struct {
	Message string `xml:"message,attr,omitempty"`
}

func secs(d time.Duration) string { return fmt.Sprintf("%.3f", d.Seconds()) }

// WriteJUnit renders suites as a JUnit XML document.
func WriteJUnit(w io.Writer, suites []Suite) error {
	doc := junitSuites{Name: "strife"}
	var total time.Duration
	for _, s := range suites {
		js := junitSuite{Name: s.Name, Tests: len(s.Cases), Time: secs(s.Time),
			Failures: s.Count(Failed), Errors: s.Count(Errored), Skipped: s.Count(Skipped)}
		if len(s.Properties) > 0 {
			js.Properties = &junitProps{}
			for _, p := range s.Properties {
				js.Properties.Props = append(js.Properties.Props, junitProp{Name: p.Name, Value: p.Value})
			}
		}
		for _, c := range s.Cases {
			jc := junitTestCase{Name: c.Name, Classname: c.Class, Time: secs(c.Time), SystemOut: c.Output}
			switch c.Status {
			case Failed:
				jc.Failure = &junitProblem{Message: c.Message, Type: "LinearizabilityViolation", Body: c.Detail}
			case Errored:
				jc.Error = &junitProblem{Message: c.Message, Type: "Inconclusive", Body: c.Detail}
			case Skipped:
				jc.Skipped = &junitSkipped{Message: c.Message}
			}
			js.Cases = append(js.Cases, jc)
		}
		doc.Tests += js.Tests
		doc.Failures += js.Failures
		doc.Errors += js.Errors
		doc.Skipped += js.Skipped
		total += s.Time
		doc.Suites = append(doc.Suites, js)
	}
	doc.Time = secs(total)
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// ---------------------------------------------------------------------------
// Markdown digest.

const maxListed = 20

// WriteSummary renders a compact Markdown digest of suites, designed for
// $GITHUB_STEP_SUMMARY: verdict first, then only what needs attention.
func WriteSummary(w io.Writer, suites []Suite) error {
	var b strings.Builder
	for _, s := range suites {
		icon := "✅"
		switch {
		case s.Count(Failed) > 0:
			icon = "❌"
		case s.Count(Errored) > 0:
			icon = "⚠️"
		}
		fmt.Fprintf(&b, "### %s Strife · %s\n\n", icon, s.Name)
		if s.Headline != "" {
			fmt.Fprintf(&b, "%s\n\n", s.Headline)
		}
		fmt.Fprintf(&b, "| Passed | Failed | Inconclusive | Skipped | Time |\n|---|---|---|---|---|\n| %d | %d | %d | %d | %v |\n\n",
			s.Count(Passed), s.Count(Failed), s.Count(Errored), s.Count(Skipped), s.Time.Round(time.Millisecond))

		var bad []Case
		for _, c := range s.Cases {
			if c.Status == Failed || c.Status == Errored {
				bad = append(bad, c)
			}
		}
		if len(bad) > 0 {
			b.WriteString("| Case | Outcome | Details |\n|---|---|---|\n")
			for i, c := range bad {
				if i == maxListed {
					fmt.Fprintf(&b, "| … | | %d more |\n", len(bad)-maxListed)
					break
				}
				outcome := "❌ violation"
				if c.Status == Errored {
					outcome = "⚠️ inconclusive"
				}
				fmt.Fprintf(&b, "| `%s` | %s | %s |\n", mdCell(c.Name), outcome, mdCell(c.Message))
			}
			b.WriteString("\n")
		}
		if s.Reproduce != "" {
			fmt.Fprintf(&b, "<details><summary>Reproduce</summary>\n\n```sh\n%s\n```\n\n</details>\n\n", s.Reproduce)
		}
		if len(s.Properties) > 0 {
			b.WriteString("<details><summary>Run details</summary>\n\n| Property | Value |\n|---|---|\n")
			for _, p := range s.Properties {
				fmt.Fprintf(&b, "| %s | `%s` |\n", mdCell(p.Name), mdCell(p.Value))
			}
			b.WriteString("\n</details>\n\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 300 {
		s = s[:297] + "..."
	}
	return s
}

// ---------------------------------------------------------------------------
// Builders.

// Properties returns the standard suite properties of a report.
func (r *Report) Properties() []Property {
	ps := []Property{
		{"strife.version", r.Version},
		{"kind", r.Kind},
		{"target", r.Target},
		{"seed", fmt.Sprint(r.Seed)},
		{"verdict", string(r.Verdict)},
		{"model", r.Check.Model},
		{"fingerprint", r.Fingerprint},
		{"history_sha256", r.HistoryHash},
	}
	if r.ScheduleHash != "" {
		ps = append(ps, Property{"schedule_sha256", r.ScheduleHash})
	}
	if r.TraceHash != "" {
		ps = append(ps, Property{"trace_sha256", r.TraceHash})
	}
	if r.Reproduce != "" {
		ps = append(ps, Property{"reproduce", r.Reproduce})
	}
	return ps
}

// KeyCases turns each checked key into a test case.
func KeyCases(class string, check checker.Report, reproduce string) []Case {
	var out []Case
	for _, k := range check.Keys {
		c := Case{Name: "key " + k.Key, Class: class, Status: Passed,
			Output: fmt.Sprintf("%d operations, %d search steps", k.Operations, k.Steps)}
		switch k.Verdict {
		case checker.Violation:
			c.Status = Failed
			c.Message = "not linearizable"
			if ce := k.Counterexample; ce != nil {
				c.Message = ce.Summary()
				c.Detail = Counterexample(ce, reproduce)
			}
		case checker.Unknown:
			c.Status = Errored
			c.Message = "search budget exhausted before a verdict (raise --max-steps)"
			if k.Reason == checker.ReasonTime {
				c.Message = "time budget exhausted before a verdict (raise --check-timeout)"
			}
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		out = append(out, Case{Name: "history", Class: class, Status: Skipped, Message: "no operations relevant to the model"})
	}
	return out
}

// Counterexample renders a counterexample as plain text.
func Counterexample(ce *checker.Counterexample, reproduce string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", ce.Summary())
	if len(ce.Witness) > 0 {
		fmt.Fprintf(&b, "\nMinimal witness (%d operations):\n", len(ce.Witness))
		for _, s := range ce.Witness {
			fmt.Fprintf(&b, "  %s [%s → %s]\n", s.Description, fmtTime(s.Call), fmtTime(s.Return))
		}
	}
	fmt.Fprintf(&b, "\nSearch: %s\nStuck: %s [%s → %s]\n", ce.Explanation, ce.Stuck.Description, fmtTime(ce.Stuck.Call), fmtTime(ce.Stuck.Return))
	lin := ce.Linearized
	if len(lin) > 10 {
		lin = lin[len(lin)-10:]
	}
	if len(lin) > 0 {
		fmt.Fprintf(&b, "\nLast %d of %d linearized operations (state after: %s):\n", len(lin), len(ce.Linearized), ce.StateAfter)
		for _, s := range lin {
			fmt.Fprintf(&b, "  %s [%s → %s]\n", s.Description, fmtTime(s.Call), fmtTime(s.Return))
		}
	}
	if reproduce != "" {
		fmt.Fprintf(&b, "\nReproduce:\n  %s\n", reproduce)
	}
	return b.String()
}

// SuiteFromReport builds the suite for a single run (simulation, live test or
// offline check): one case per key, plus, for live runs, one case for fault
// injection itself.
func SuiteFromReport(r *Report, elapsed time.Duration) Suite {
	class := r.Target
	if r.Kind == KindSimulation || r.Kind == KindLive {
		class = fmt.Sprintf("%s.seed%d", r.Target, r.Seed)
	}
	s := Suite{
		Name:       fmt.Sprintf("%s · %s", r.Kind, r.Target),
		Time:       elapsed,
		Properties: r.Properties(),
		Cases:      KeyCases(class, r.Check, r.Reproduce),
		Reproduce:  r.Reproduce,
	}
	st := r.Check.Stats
	s.Headline = fmt.Sprintf("**%s** · %d operations (%d ok, %d failed, %d indeterminate) · %d faults",
		strings.ToUpper(string(r.Verdict)), st.Invocations, st.OK, st.Fail, st.Info, len(r.Schedule.Faults))
	if sh := r.Shrink; sh != nil {
		s.Headline += fmt.Sprintf(" · shrunk from %d to %d faults in %d runs", sh.OriginalFaults, sh.MinimalFaults, sh.Runs)
	}
	if r.Kind == KindLive {
		inj := Case{Name: "fault injection", Class: class, Status: Passed,
			Output: fmt.Sprintf("%d fault edges applied", len(r.Applied))}
		var errs []string
		for _, a := range r.Applied {
			for _, e := range a.Errors {
				errs = append(errs, fmt.Sprintf("fault %d (%s, start=%v): %s", a.FaultID, a.Kind, a.Start, e))
			}
		}
		if len(errs) > 0 {
			// The verdict may still be right, but the cluster did not see
			// the planned faults, so the run did not test what it claims.
			inj.Status = Errored
			inj.Message = fmt.Sprintf("%d fault commands failed", len(errs))
			inj.Detail = strings.Join(errs, "\n")
		}
		s.Cases = append(s.Cases, inj)
	}
	return s
}

// SeedResult is one explored seed, for building exploration suites.
type SeedResult struct {
	Seed        uint64
	Verdict     checker.Verdict
	Ops         int
	Faults      int
	Explanation string
	Reproduce   string
	Error       string
	Elapsed     time.Duration
}

// ExplorationSuite builds the suite for a seed exploration. In bug-hunting
// mode (expectViolation) the suite holds a single case that passes when any
// seed violates, because finding the bug is the point of the test.
func ExplorationSuite(system, faults string, seeds []SeedResult, expectViolation bool, elapsed time.Duration) Suite {
	sort.Slice(seeds, func(i, j int) bool { return seeds[i].Seed < seeds[j].Seed })
	s := Suite{Name: fmt.Sprintf("simulation · %s", system), Time: elapsed,
		Properties: []Property{{"strife.version", Version}, {"system", system}, {"faults", faults}, {"seeds", fmt.Sprint(len(seeds))}}}
	var violating []uint64
	var firstRepro string
	for _, r := range seeds {
		if r.Verdict == checker.Violation {
			violating = append(violating, r.Seed)
			if firstRepro == "" {
				firstRepro = r.Reproduce
			}
		}
	}
	if len(seeds) > 0 {
		s.Properties = append(s.Properties, Property{"seed_range", fmt.Sprintf("%d..%d", seeds[0].Seed, seeds[len(seeds)-1].Seed)})
	}
	s.Headline = fmt.Sprintf("%d seeds of `%s` under `%s`: **%d violating**", len(seeds), system, faults, len(violating))
	s.Reproduce = firstRepro

	if expectViolation {
		c := Case{Name: "finds a violation", Class: system, Time: elapsed}
		if len(violating) > 0 {
			c.Status = Passed
			c.Output = fmt.Sprintf("violating seeds: %v\nreproduce: %s", violating, firstRepro)
		} else {
			c.Status = Failed
			c.Message = fmt.Sprintf("expected a violation in %d seeds, found none", len(seeds))
		}
		for _, r := range seeds {
			if r.Error != "" || r.Verdict == checker.Unknown {
				c.Output += fmt.Sprintf("\nseed %d inconclusive: %s", r.Seed, r.Error)
			}
		}
		s.Cases = []Case{c}
		return s
	}

	for _, r := range seeds {
		c := Case{Name: fmt.Sprintf("seed %d", r.Seed), Class: system, Time: r.Elapsed, Status: Passed,
			Output: fmt.Sprintf("%d operations, %d faults", r.Ops, r.Faults)}
		switch {
		case r.Error != "":
			c.Status, c.Message = Errored, r.Error
		case r.Verdict == checker.Violation:
			c.Status = Failed
			c.Message = r.Explanation
			if c.Message == "" {
				c.Message = "not linearizable"
			}
			c.Detail = fmt.Sprintf("%s\n\nReproduce:\n  %s\n", r.Explanation, r.Reproduce)
		case r.Verdict == checker.Unknown:
			c.Status, c.Message = Errored, "search budget exhausted before a verdict"
		}
		s.Cases = append(s.Cases, c)
	}
	return s
}

// ErrorSuite reports a run that could not complete at all.
func ErrorSuite(name string, err error) Suite {
	return Suite{Name: name, Headline: "The run did not complete.",
		Cases: []Case{{Name: "run", Class: name, Status: Errored, Message: err.Error()}}}
}

// ---------------------------------------------------------------------------

// Outputs writes CI artifacts. Empty paths are skipped. The summary is
// appended to, as $GITHUB_STEP_SUMMARY expects; the JUnit file is replaced.
type Outputs struct {
	JUnit   string
	Summary string
}

// Write emits suites to every configured output.
func (o Outputs) Write(suites []Suite) error {
	if o.JUnit != "" {
		f, err := os.Create(o.JUnit)
		if err != nil {
			return err
		}
		if err := WriteJUnit(f, suites); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	if o.Summary != "" {
		f, err := os.OpenFile(o.Summary, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if err := WriteSummary(f, suites); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	return nil
}

// Enabled reports whether any output is configured.
func (o Outputs) Enabled() bool { return o.JUnit != "" || o.Summary != "" }
