// Command strife is a deterministic chaos testing engine and linearizability
// checker for distributed systems.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/explore"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/report"
	"github.com/kemalhafizh/strife/pkg/runner"
	"github.com/kemalhafizh/strife/pkg/shrink"
	"github.com/kemalhafizh/strife/pkg/transport"
)

// Exit codes.
const (
	exitOK        = 0
	exitViolation = 1
	exitUsage     = 2
	exitUnknown   = 3
	exitError     = 4
)

const usage = `Strife — deterministic chaos testing & linearizability checking

Usage:
  strife sim     [flags]   run systems in the deterministic simulator
  strife shrink  [flags]   minimise a failing simulation to its essential faults
  strife check   [flags]   check a recorded history (JSONL) for linearizability
  strife plan    [flags]   print a seeded fault schedule and its shell commands
  strife run     [flags]   run a live test against a real cluster
  strife systems           list the built-in simulated systems
  strife version

Run "strife <command> -h" for flags.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	var err error
	code := exitOK
	switch cmd {
	case "sim":
		code, err = cmdSim(rest, stdout, stderr)
	case "shrink":
		code, err = cmdShrink(rest, stdout, stderr)
	case "check":
		code, err = cmdCheck(rest, stdout, stderr)
	case "plan":
		code, err = cmdPlan(rest, stdout, stderr)
	case "run":
		code, err = cmdRun(rest, stdout, stderr)
	case "systems":
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SYSTEM\tEXPECTED\tEXPOSED BY\tDESCRIPTION")
		for _, s := range explore.Systems() {
			hint := s.Hint
			if hint == "" {
				hint = "-"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, s.Expect, hint, s.Description)
		}
		tw.Flush()
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "strife %s\n", report.Version)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
	default:
		fmt.Fprintf(stderr, "strife: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		fmt.Fprintf(stderr, "strife %s: %v\n", cmd, err)
		if code == exitOK {
			code = exitError
		}
	}
	return code
}

func verdictCode(v checker.Verdict) int {
	switch v {
	case checker.Linearizable:
		return exitOK
	case checker.Violation:
		return exitViolation
	}
	return exitUnknown
}

// ---------------------------------------------------------------------------

// scenarioFlags binds the flags describing one simulated scenario and returns
// a function that builds the config once the flags are parsed.
func scenarioFlags(fs *flag.FlagSet) func() (explore.Config, error) {
	d := explore.DefaultConfig()
	system := fs.String("system", d.System, "system under test (see `strife systems`)")
	seed := fs.Uint64("seed", d.Seed, "seed for a single run")
	nodes := fs.Int("nodes", d.Nodes, "cluster size")
	clients := fs.Int("clients", d.Clients, "concurrent client processes")
	keys := fs.Int("keys", d.Keys, "independent keys")
	values := fs.Int("values", d.Values, "values are drawn from [0, values)")
	duration := fs.Duration("duration", d.Duration, "simulated test length")
	readRatio := fs.Float64("read-ratio", d.ReadRatio, "fraction of reads")
	casRatio := fs.Float64("cas-ratio", d.CasRatio, "fraction of compare-and-sets")
	opTimeout := fs.Duration("op-timeout", d.OpTimeout, "client operation timeout (simulated)")
	faults := fs.String("faults", "all", "comma separated fault kinds, \"all\" or \"none\"")
	interval := fs.Duration("fault-interval", d.FaultInterval, "mean time between faults")
	length := fs.Duration("fault-length", d.FaultLength, "mean fault length")
	planPath := fs.String("plan", "", "replay this schedule.json instead of generating one")
	maxSteps := fs.Uint64("max-steps", 0, "checker search budget per key (0 = default)")
	checkTimeout := fs.Duration("check-timeout", 0, "checker wall-clock budget per key (0 = none; makes verdicts machine dependent)")
	return func() (explore.Config, error) {
		kinds, err := nemesis.ParseKinds(*faults)
		if err != nil {
			return explore.Config{}, err
		}
		cfg := d
		cfg.System, cfg.Seed, cfg.Nodes, cfg.Clients, cfg.Keys, cfg.Values = *system, *seed, *nodes, *clients, *keys, *values
		cfg.Duration, cfg.ReadRatio, cfg.CasRatio, cfg.OpTimeout = *duration, *readRatio, *casRatio, *opTimeout
		cfg.Faults, cfg.FaultInterval, cfg.FaultLength, cfg.MaxSteps = kinds, *interval, *length, *maxSteps
		cfg.CheckTimeout = *checkTimeout
		if *planPath != "" {
			data, err := os.ReadFile(*planPath)
			if err != nil {
				return explore.Config{}, err
			}
			var plan nemesis.Schedule
			if err := json.Unmarshal(data, &plan); err != nil {
				return explore.Config{}, fmt.Errorf("%s: %w", *planPath, err)
			}
			cfg.Plan = &plan
		}
		return cfg, cfg.Validate()
	}
}

func cmdSim(args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("sim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scenario := scenarioFlags(fs)
	seeds := fs.String("seeds", "", "explore many seeds: \"1..1000\" or \"3,7,9\"")
	out := fs.String("out", "strife-out", "directory for reports (\"\" to disable)")
	keep := fs.Int("keep", 3, "when exploring, write reports for this many failing seeds")
	workers := fs.Int("workers", 0, "parallel simulations (default: GOMAXPROCS)")
	determinism := fs.Bool("determinism", false, "run every seed twice and fail on any divergence")
	expect := fs.String("expect", "", "\"linearizable\" or \"violation\": exit 0 iff the outcome matches (for CI)")
	shrinkIt := fs.Bool("shrink", false, "minimise the (first) violating scenario and report it")
	ci := ciFlags(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage, err
	}
	cfg, err := scenario()
	if err != nil {
		return exitUsage, err
	}
	if *expect != "" && *expect != string(checker.Linearizable) && *expect != string(checker.Violation) {
		return exitUsage, fmt.Errorf("--expect must be linearizable or violation")
	}

	o := simOptions{out: *out, keep: *keep, workers: *workers, determinism: *determinism, expect: *expect, ci: *ci}
	if *shrinkIt {
		o.shrink = &shrink.Options{Workers: *workers}
	}
	if *seeds == "" {
		return simOne(cfg, o, stdout)
	}
	list, err := explore.ParseSeeds(*seeds)
	if err != nil {
		return exitUsage, err
	}
	return simMany(cfg, list, o, stdout)
}

// simOptions are the sim command's behaviour switches.
type simOptions struct {
	out         string
	keep        int
	workers     int
	determinism bool
	expect      string
	shrink      *shrink.Options
	ci          report.Outputs
}

// ciFlags binds --junit and --summary, shared by every command that reaches
// a verdict.
func ciFlags(fs *flag.FlagSet) *report.Outputs {
	o := &report.Outputs{}
	fs.StringVar(&o.JUnit, "junit", "", "write a JUnit XML report here (for CI test reporting)")
	fs.StringVar(&o.Summary, "summary", "", "append a Markdown summary here (e.g. \"$GITHUB_STEP_SUMMARY\")")
	return o
}

// emitCI writes CI artifacts and folds a write failure into the command's
// error, without masking an earlier one.
func emitCI(ci report.Outputs, code int, err error, suites ...report.Suite) (int, error) {
	if !ci.Enabled() {
		return code, err
	}
	if werr := ci.Write(suites); werr != nil {
		werr = fmt.Errorf("writing CI outputs: %w", werr)
		if err == nil {
			return exitError, werr
		}
		return code, errors.Join(err, werr)
	}
	return code, err
}

func reproduce(c explore.Config, planPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "strife sim --system %s --seed %d --nodes %d --clients %d --keys %d --values %d --duration %v --op-timeout %v",
		c.System, c.Seed, c.Nodes, c.Clients, c.Keys, c.Values, c.Duration, c.OpTimeout)
	fmt.Fprintf(&b, " --read-ratio %g --cas-ratio %g --fault-interval %v --fault-length %v", c.ReadRatio, c.CasRatio, c.FaultInterval, c.FaultLength)
	ks := make([]string, len(c.Faults))
	for i, k := range c.Faults {
		ks[i] = string(k)
	}
	f := strings.Join(ks, ",")
	if f == "" {
		f = "none"
	}
	fmt.Fprintf(&b, " --faults %s", f)
	if c.MaxSteps != 0 {
		fmt.Fprintf(&b, " --max-steps %d", c.MaxSteps)
	}
	if c.CheckTimeout != 0 {
		fmt.Fprintf(&b, " --check-timeout %v", c.CheckTimeout)
	}
	if c.Plan != nil {
		if planPath == "" {
			planPath = "schedule.json"
		}
		if strings.ContainsAny(planPath, " \t'\"$`\\;&|<>()*?[]{}!#~") {
			planPath = transport.Quote(planPath)
		}
		fmt.Fprintf(&b, " --plan %s", planPath)
	}
	return b.String()
}

func simReport(res *explore.Result, dir string) (*report.Report, error) {
	r, err := report.New(report.KindSimulation, res.Config.System, res.Config.Seed, res.Config, res.Schedule, res.History, res.Check, res.TraceHash)
	if err != nil {
		return nil, err
	}
	r.Events = res.Events
	r.Reproduce = reproduce(res.Config, filepath.Join(dir, "schedule.json"))
	r.Fingerprint = report.Fingerprint(r)
	return r, nil
}

func writeSimReport(res *explore.Result, dir string) (string, error) {
	addWitnesses(model.KV{}, res.History, &res.Check)
	r, err := simReport(res, dir)
	if err != nil {
		return "", err
	}
	if _, err := report.WriteDir(dir, r, res.History); err != nil {
		return "", err
	}
	return filepath.Join(dir, "report.html"), nil
}

func simOne(cfg explore.Config, o simOptions, stdout io.Writer) (int, error) {
	start := time.Now()
	suiteName := fmt.Sprintf("simulation · %s", cfg.System)
	var res *explore.Result
	if o.determinism {
		a, b, ok, err := explore.Determinism(cfg)
		if err != nil {
			return emitCI(o.ci, exitError, err, report.ErrorSuite(suiteName, err))
		}
		fmt.Fprintf(stdout, "determinism: run 1 trace %s\n             run 2 trace %s\n", short(a.TraceHash), short(b.TraceHash))
		if !ok {
			err := fmt.Errorf("NONDETERMINISM: identical seeds produced different executions (trace %s vs %s)", short(a.TraceHash), short(b.TraceHash))
			return emitCI(o.ci, exitError, err, report.ErrorSuite(suiteName, err))
		}
		fmt.Fprintln(stdout, "determinism: ✓ identical executions")
		res = a
	} else {
		var err error
		if res, err = explore.Run(cfg); err != nil {
			return emitCI(o.ci, exitError, err, report.ErrorSuite(suiteName, err))
		}
	}
	addWitnesses(model.KV{}, res.History, &res.Check)
	printResult(stdout, res)
	dir := ""
	if o.out != "" {
		dir = filepath.Join(o.out, fmt.Sprintf("%s-seed%d", cfg.System, cfg.Seed))
	}
	rep, err := simReport(res, dir)
	if err != nil {
		return exitError, err
	}
	if dir != "" {
		if _, err := report.WriteDir(dir, rep, res.History); err != nil {
			return exitError, err
		}
		fmt.Fprintf(stdout, "report:  %s\n", filepath.Join(dir, "report.html"))
	}
	if o.shrink != nil && res.Check.Verdict == checker.Violation {
		if _, err := runShrink(cfg, *o.shrink, o.out, stdout); err != nil {
			return exitError, err
		}
	}

	suite := report.SuiteFromReport(rep, time.Since(start))
	code, verr := verdictCode(res.Check.Verdict), error(nil)
	if o.expect != "" {
		// The expectation is the test: a violation that was expected passes.
		c := report.Case{Name: "expect " + o.expect, Class: suite.Cases[0].Class, Time: time.Since(start), Status: report.Passed}
		if string(res.Check.Verdict) == o.expect {
			code = exitOK
		} else {
			code, verr = exitViolation, fmt.Errorf("expected %s, got %s", o.expect, res.Check.Verdict)
			c.Status, c.Message = report.Failed, verr.Error()
		}
		c.Output = fmt.Sprintf("verdict: %s", res.Check.Verdict)
		suite.Cases = []report.Case{c}
	}
	return emitCI(o.ci, code, verr, suite)
}

func short(h string) string {
	if len(h) > 16 {
		return h[:16]
	}
	return h
}

func printResult(w io.Writer, res *explore.Result) {
	c := res.Check
	fmt.Fprintf(w, "system:  %s   seed: %d   nodes: %d   simulated: %v   events: %d   wall: %v\n",
		res.Config.System, res.Config.Seed, res.Config.Nodes, res.Config.Duration, res.Events, res.Elapsed.Round(time.Millisecond))
	fmt.Fprintf(w, "faults:  %d scheduled (schedule %s)\n", len(res.Schedule.Faults), short(res.ScheduleHash))
	for _, f := range res.Schedule.Faults {
		fmt.Fprintf(w, "         %7.3fs → %7.3fs  %s\n", f.Start.Seconds(), f.End.Seconds(), f.Describe(res.Config.Nodes))
	}
	fmt.Fprintf(w, "ops:     %d (%d ok, %d fail, %d info)   history %s   trace %s\n",
		c.Stats.Invocations, c.Stats.OK, c.Stats.Fail, c.Stats.Info, short(res.HistoryHash), short(res.TraceHash))
	fmt.Fprintf(w, "verdict: %s\n", strings.ToUpper(string(c.Verdict)))
	for _, k := range c.Failures() {
		if ce := k.Counterexample; ce != nil {
			fmt.Fprintf(w, "  key %s: %s\n", k.Key, ce.Summary())
		}
	}
}

func simMany(base explore.Config, seeds []uint64, o simOptions, stdout io.Writer) (int, error) {
	start := time.Now()
	sys, _ := explore.LookupSystem(base.System)
	faults := faultList(base)
	suiteName := fmt.Sprintf("simulation · %s", base.System)
	fmt.Fprintf(stdout, "exploring %d seeds of %s (%s) with faults %s\n", len(seeds), base.System, sys.Description, faults)
	var kept []string
	sums := explore.Explore(base, seeds, o.workers, func(res *explore.Result) {
		if res.Check.Verdict == checker.Linearizable || o.out == "" || len(kept) >= o.keep {
			return
		}
		dir := filepath.Join(o.out, fmt.Sprintf("%s-seed%d", res.Config.System, res.Config.Seed))
		if p, err := writeSimReport(res, dir); err == nil {
			kept = append(kept, p)
		}
	})
	counts := map[checker.Verdict]int{}
	var failing []uint64
	var errs []string
	for _, s := range sums {
		if s.Error != "" {
			errs = append(errs, fmt.Sprintf("seed %d: %s", s.Seed, s.Error))
			continue
		}
		counts[s.Verdict]++
		if s.Verdict == checker.Violation {
			failing = append(failing, s.Seed)
		}
	}
	if o.determinism {
		fmt.Fprintln(stdout, "determinism: re-running every seed...")
		again := explore.Explore(base, seeds, o.workers, nil)
		for i := range sums {
			if sums[i].TraceHash != again[i].TraceHash {
				err := fmt.Errorf("NONDETERMINISM at seed %d: trace %s vs %s", sums[i].Seed, short(sums[i].TraceHash), short(again[i].TraceHash))
				return emitCI(o.ci, exitError, err, report.ErrorSuite(suiteName, err))
			}
		}
		fmt.Fprintf(stdout, "determinism: ✓ %d seeds replayed identically\n", len(seeds))
	}
	fmt.Fprintf(stdout, "done in %v: %d linearizable, %d violations, %d unknown, %d errors\n",
		time.Since(start).Round(time.Millisecond), counts[checker.Linearizable], counts[checker.Violation], counts[checker.Unknown], len(errs))
	for _, e := range errs {
		fmt.Fprintln(stdout, "  error:", e)
	}
	if len(failing) > 0 {
		show := failing
		if len(show) > 20 {
			show = show[:20]
		}
		fmt.Fprintf(stdout, "violating seeds: %v", show)
		if len(failing) > len(show) {
			fmt.Fprintf(stdout, " … (+%d)", len(failing)-len(show))
		}
		fmt.Fprintln(stdout)
		c := base
		c.Seed = failing[0]
		fmt.Fprintf(stdout, "reproduce: %s\n", reproduce(c, ""))
	}
	for _, p := range kept {
		fmt.Fprintf(stdout, "report:  %s\n", p)
	}
	if o.shrink != nil && len(failing) > 0 {
		c := base
		c.Seed = failing[0]
		if _, err := runShrink(c, *o.shrink, o.out, stdout); err != nil {
			return exitError, err
		}
	}

	results := make([]report.SeedResult, len(sums))
	for i, s := range sums {
		c := base
		c.Seed = s.Seed
		results[i] = report.SeedResult{Seed: s.Seed, Verdict: s.Verdict, Ops: s.Ops, Faults: s.Faults,
			Explanation: s.Explanation, Reproduce: reproduce(c, ""), Error: s.Error, Elapsed: s.Elapsed}
	}
	suite := report.ExplorationSuite(base.System, faults, results, o.expect == string(checker.Violation), time.Since(start))

	var code int
	var err error
	switch {
	case len(errs) > 0:
		code, err = exitError, fmt.Errorf("%d seeds failed to run", len(errs))
	case o.expect == string(checker.Violation):
		if len(failing) == 0 {
			code, err = exitViolation, fmt.Errorf("expected a violation, none found")
		}
	case o.expect == string(checker.Linearizable):
		if len(failing) > 0 || counts[checker.Unknown] > 0 {
			code, err = exitViolation, fmt.Errorf("expected every seed to be linearizable")
		}
	case len(failing) > 0:
		code = exitViolation
	case counts[checker.Unknown] > 0:
		code = exitUnknown
	}
	return emitCI(o.ci, code, err, suite)
}

func faultList(c explore.Config) string {
	if c.Plan != nil {
		return "plan"
	}
	ks := make([]string, len(c.Faults))
	for i, k := range c.Faults {
		ks[i] = string(k)
	}
	if len(ks) == 0 {
		return "none"
	}
	return strings.Join(ks, ",")
}

// ---------------------------------------------------------------------------

func cmdShrink(args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("shrink", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scenario := scenarioFlags(fs)
	from := fs.String("report", "", "shrink the scenario recorded in this simulation report.json (overrides scenario flags)")
	out := fs.String("out", "strife-out", "directory for the minimal report (\"\" to disable)")
	maxRuns := fs.Int("max-runs", 600, "simulation budget")
	workers := fs.Int("workers", 0, "parallel simulations (default: GOMAXPROCS)")
	keepWorkload := fs.Bool("keep-workload", false, "only shrink the fault schedule, not duration or clients")
	ci := ciFlags(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage, err
	}
	var cfg explore.Config
	if *from != "" {
		data, err := os.ReadFile(*from)
		if err != nil {
			return exitUsage, err
		}
		var rep report.Report
		if err := json.Unmarshal(data, &rep); err != nil {
			return exitUsage, fmt.Errorf("%s: %w", *from, err)
		}
		if rep.Kind != report.KindSimulation {
			return exitUsage, fmt.Errorf("%s: only simulation reports can be shrunk (this one is %q)", *from, rep.Kind)
		}
		if err := json.Unmarshal(rep.Config, &cfg); err != nil {
			return exitUsage, fmt.Errorf("%s: config: %w", *from, err)
		}
		if err := cfg.Validate(); err != nil {
			return exitUsage, err
		}
	} else {
		var err error
		if cfg, err = scenario(); err != nil {
			return exitUsage, err
		}
	}
	start := time.Now()
	rep, err := runShrink(cfg, shrink.Options{MaxRuns: *maxRuns, Workers: *workers, KeepWorkload: *keepWorkload}, *out, stdout)
	name := fmt.Sprintf("shrink · %s", cfg.System)
	if errors.Is(err, shrink.ErrNotViolating) {
		return emitCI(*ci, exitUsage, err, report.ErrorSuite(name, err))
	}
	if err != nil {
		return emitCI(*ci, exitError, err, report.ErrorSuite(name, err))
	}
	// The minimal scenario is a reproducer of a failure, so its keys fail.
	suite := report.SuiteFromReport(rep, time.Since(start))
	suite.Name = name
	return emitCI(*ci, exitViolation, nil, suite)
}

// runShrink minimises cfg, prints the outcome and writes the minimal report.
func runShrink(cfg explore.Config, opts shrink.Options, out string, stdout io.Writer) (*report.Report, error) {
	fmt.Fprintf(stdout, "\nshrinking %s seed %d…\n", cfg.System, cfg.Seed)
	opts.Progress = func(s shrink.Step) {
		fmt.Fprintf(stdout, "  %-9s %3d runs  faults %-3d %-9v clients %-3d %s\n",
			s.Phase, s.Runs, s.Faults, s.Duration.Round(time.Millisecond), s.Clients, s.Note)
	}
	res, err := shrink.Shrink(context.Background(), cfg, opts)
	if err != nil {
		return nil, err
	}
	min := res.Minimal
	addWitnesses(model.KV{}, res.Final.History, &res.Final.Check)
	fmt.Fprintf(stdout, "minimal: %d → %d faults, %v → %v, %d → %d clients, %d → %d ops (%d runs, %v)\n",
		len(res.OriginalSchedule.Faults), len(res.Faults()), res.Original.Duration, min.Duration,
		res.Original.Clients, min.Clients, res.OriginalOps, res.Final.Check.Operations, res.Runs, res.Elapsed.Round(time.Millisecond))
	for _, f := range res.Faults() {
		fmt.Fprintf(stdout, "         #%-3d %-13v → %-13v %s\n", f.ID, f.Start, f.End, f.Describe(min.Nodes))
	}
	for _, k := range res.Final.Check.Failures() {
		if ce := k.Counterexample; ce != nil {
			fmt.Fprintf(stdout, "  key %s: %s\n", k.Key, ce.Summary())
			break
		}
	}
	dir := ""
	if out != "" {
		dir = filepath.Join(out, fmt.Sprintf("%s-seed%d-min", min.System, min.Seed))
	}
	rep, err := simReport(res.Final, dir)
	if err != nil {
		return nil, err
	}
	info := &report.ShrinkInfo{
		OriginalFaults: len(res.OriginalSchedule.Faults), MinimalFaults: len(res.Faults()),
		OriginalOps: res.OriginalOps, MinimalOps: res.Final.Check.Operations,
		OriginalDuration: res.Original.Duration, MinimalDuration: min.Duration,
		OriginalClients: res.Original.Clients, MinimalClients: min.Clients,
		OriginalScheduleHash: res.OriginalSchedule.Hash(),
		Runs:                 res.Runs, Elapsed: res.Elapsed, BudgetExhausted: res.BudgetExhausted,
	}
	for _, s := range res.Steps {
		info.Steps = append(info.Steps, fmt.Sprintf("%s: %s", s.Phase, s.Note))
	}
	rep.Shrink = info
	if dir == "" {
		// Nothing is saved, so there is no schedule file to replay from.
		rep.Reproduce = ""
		fmt.Fprintln(stdout, "(pass --out to save the minimal schedule and a reproduction command)")
		return rep, nil
	}
	if _, err := report.WriteDir(dir, rep, res.Final.History); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(dir, "shrink.json"))
	if err != nil {
		return nil, err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	fmt.Fprintf(stdout, "reproduce: %s\nreport:  %s\n", rep.Reproduce, filepath.Join(dir, "report.html"))
	return rep, nil
}

// ---------------------------------------------------------------------------

func cmdCheck(args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("history", "", "history file (JSONL, one op per line; - for stdin)")
	modelName := fs.String("model", "kv", "consistency model: "+strings.Join(model.Names(), ", "))
	out := fs.String("out", "", "write a report directory here")
	maxSteps := fs.Uint64("max-steps", 0, "search budget per key (0 = default)")
	timeout := fs.Duration("check-timeout", 0, "wall-clock budget per key (0 = none)")
	ci := ciFlags(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage, err
	}
	if *path == "" && fs.NArg() > 0 {
		*path = fs.Arg(0)
	}
	if *path == "" {
		return exitUsage, fmt.Errorf("--history is required")
	}
	m, err := model.ByName(*modelName)
	if err != nil {
		return exitUsage, err
	}
	var r io.Reader = os.Stdin
	if *path != "-" {
		f, err := os.Open(*path)
		if err != nil {
			return exitError, err
		}
		defer f.Close()
		r = f
	}
	ops, err := history.ReadJSONL(r)
	if err != nil {
		return exitError, err
	}
	start := time.Now()
	rep, err := checker.Check(m, ops, checker.Options{MaxSteps: *maxSteps, Timeout: *timeout})
	if err != nil {
		return exitError, err
	}
	addWitnesses(m, ops, &rep)
	fmt.Fprintf(stdout, "checked %d operations (%d relevant) across %d keys in %v\n", rep.Operations, rep.Checked, len(rep.Keys), time.Since(start).Round(time.Millisecond))
	fmt.Fprintf(stdout, "verdict: %s\n", strings.ToUpper(string(rep.Verdict)))
	for _, k := range rep.Failures() {
		if ce := k.Counterexample; ce != nil {
			fmt.Fprintf(stdout, "  key %s: %s\n", k.Key, ce.Summary())
		}
	}
	rr, err := report.New(report.KindCheck, filepath.Base(*path), 0, map[string]string{"model": m.Name()}, nemesis.Schedule{}, ops, rep, "")
	if err != nil {
		return exitError, err
	}
	if *out != "" {
		if _, err := report.WriteDir(*out, rr, ops); err != nil {
			return exitError, err
		}
		fmt.Fprintf(stdout, "report:  %s\n", filepath.Join(*out, "report.html"))
	}
	return emitCI(*ci, verdictCode(rep.Verdict), nil, report.SuiteFromReport(rr, time.Since(start)))
}

// ---------------------------------------------------------------------------

func cmdPlan(args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "live config (strife.json); prints the exact commands that would run")
	seed := fs.Uint64("seed", 1, "seed (overrides the config's)")
	nodes := fs.Int("nodes", 5, "cluster size (without --config)")
	duration := fs.Duration("duration", 60*time.Second, "test length (overrides the config's)")
	faults := fs.String("faults", "all", "fault kinds (without --config)")
	interval := fs.Duration("fault-interval", 6*time.Second, "mean time between faults (without --config)")
	length := fs.Duration("fault-length", 6*time.Second, "mean fault length (without --config)")
	asJSON := fs.Bool("json", false, "print the schedule as JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage, err
	}
	var sched nemesis.Schedule
	var cfg *runner.Config
	if *cfgPath != "" {
		var err error
		if cfg, err = runner.Load(*cfgPath); err != nil {
			return exitUsage, err
		}
		// Explicit --seed / --duration override the config, as they do for run.
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "seed":
				cfg.Seed = *seed
			case "duration":
				cfg.Duration = runner.Duration(*duration)
			}
		})
		sched = cfg.Schedule()
	} else {
		kinds, err := nemesis.ParseKinds(*faults)
		if err != nil {
			return exitUsage, err
		}
		sched = nemesis.Generate(*seed, nemesis.Options{Nodes: *nodes, Duration: *duration, Kinds: kinds, Interval: *interval, Length: *length})
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return exitOK, enc.Encode(sched)
	}
	fmt.Fprintf(stdout, "seed %d · %d nodes · %v · %d faults · schedule sha256 %s\n\n", sched.Seed, sched.Nodes, sched.Duration, len(sched.Faults), sched.Hash())
	var env nemesis.Env
	if cfg != nil {
		env = cfg.Env()
		for _, c := range env.Setup() {
			fmt.Fprintf(stdout, "  setup  [%s] %s\n", cfg.Nodes[c.Node].Name, c.Script)
		}
		fmt.Fprintln(stdout)
	}
	for _, a := range sched.Timeline() {
		verb := "inject"
		if !a.Start {
			verb = "heal  "
		}
		fmt.Fprintf(stdout, "%8.2fs %s #%d %s\n", a.At.Seconds(), verb, a.Fault.ID, a.Fault.Describe(sched.Nodes))
		if cfg == nil {
			continue
		}
		cmds := env.Inject(a.Fault)
		if !a.Start {
			cmds = env.Heal(a.Fault)
		}
		for _, c := range cmds {
			where := cfg.Nodes[c.Node].Name
			if c.Host {
				where = "host of " + where
			}
			fmt.Fprintf(stdout, "            [%s] %s\n", where, c.Script)
		}
	}
	return exitOK, nil
}

// ---------------------------------------------------------------------------

func cmdRun(args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "strife.json", "live test config")
	out := fs.String("out", "strife-out", "directory for reports")
	dry := fs.Bool("dry-run", false, "log fault commands instead of executing them")
	seed := fs.Int64("seed", -1, "override the config seed")
	duration := fs.Duration("duration", 0, "override the config duration")
	quiet := fs.Bool("quiet", false, "less progress output")
	expect := fs.String("expect", "", "\"linearizable\" or \"violation\": exit 0 iff the verdict matches (for CI)")
	faults := fs.String("faults", "", "override the config's fault kinds (comma separated, \"none\" for a baseline)")
	ci := ciFlags(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage, err
	}
	cfg, err := runner.Load(*cfgPath)
	if err != nil {
		return exitUsage, err
	}
	if *expect != "" && *expect != string(checker.Linearizable) && *expect != string(checker.Violation) {
		return exitUsage, fmt.Errorf("--expect must be linearizable or violation")
	}
	if *seed >= 0 {
		cfg.Seed = uint64(*seed)
	}
	if *duration > 0 {
		cfg.Duration = runner.Duration(*duration)
	}
	if *faults != "" {
		kinds, err := nemesis.ParseKinds(*faults)
		if err != nil {
			return exitUsage, err
		}
		cfg.Nemesis.Faults = kinds
		if err := cfg.Validate(); err != nil {
			return exitUsage, err
		}
	}
	opts := runner.Options{}
	if !*quiet {
		opts.Logf = func(format string, a ...any) { fmt.Fprintf(stdout, format+"\n", a...) }
	}
	var dryLog *transport.DryRun
	if *dry {
		dryLog = &transport.DryRun{}
		opts.Transport = dryLog
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(stdout, "strife run %q: seed %d, %d nodes, %v, faults %v\n", cfg.Name, cfg.Seed, len(cfg.Nodes), time.Duration(cfg.Duration), cfg.Nemesis.Faults)
	start := time.Now()
	res, err := runner.Run(ctx, cfg, opts)
	if err != nil {
		return emitCI(*ci, exitError, err, report.ErrorSuite("live · "+cfg.Name, err))
	}
	addWitnesses(model.KV{}, res.History, &res.Check)
	rep, err := report.New(report.KindLive, cfg.Name, cfg.Seed, cfg, res.Schedule, res.History, res.Check, "")
	if err != nil {
		return exitError, err
	}
	rep.Applied = res.Applied
	rep.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	rep.Reproduce = fmt.Sprintf("strife run --config %s --seed %d", *cfgPath, cfg.Seed)
	rep.Notes = append(rep.Notes, "Live runs replay the same fault schedule and per-process operation sequence for a given seed; the interleaving chosen by the real cluster is recorded in history.jsonl and can be re-checked with `strife check`.")
	if res.Namespace != "" {
		rep.Notes = append(rep.Notes, fmt.Sprintf("Keys were isolated under the per-run namespace %q, so values left by earlier runs cannot leak into this history.", res.Namespace))
	}
	if *dry {
		rep.Notes = append(rep.Notes, "Dry run: fault commands were logged, not executed.")
	}
	rep.Fingerprint = report.Fingerprint(rep)
	dir := filepath.Join(*out, fmt.Sprintf("%s-seed%d-%s", cfg.Name, cfg.Seed, time.Now().UTC().Format("20060102T150405Z")))
	if _, err := report.WriteDir(dir, rep, res.History); err != nil {
		return exitError, err
	}
	if dryLog != nil {
		if err := os.WriteFile(filepath.Join(dir, "commands.log"), []byte(strings.Join(dryLog.Log, "\n")+"\n"), 0o644); err != nil {
			return exitError, err
		}
	}
	c := res.Check
	fmt.Fprintf(stdout, "ops: %d (%d ok, %d fail, %d info)\nverdict: %s\nreport: %s\n",
		c.Stats.Invocations, c.Stats.OK, c.Stats.Fail, c.Stats.Info, strings.ToUpper(string(c.Verdict)), filepath.Join(dir, "report.html"))
	for _, k := range c.Failures() {
		if ce := k.Counterexample; ce != nil {
			fmt.Fprintf(stdout, "  key %s: %s\n", k.Key, ce.Summary())
		}
	}
	suite := report.SuiteFromReport(rep, time.Since(start))
	code, rerr := verdictCode(c.Verdict), error(nil)
	if *expect != "" {
		// The expectation is the verdict test case; fault injection stays.
		vc := report.Case{Name: "expect " + *expect, Class: suite.Cases[0].Class, Time: time.Since(start), Status: report.Passed,
			Output: fmt.Sprintf("verdict: %s", c.Verdict)}
		if string(c.Verdict) == *expect {
			code = exitOK
		} else {
			code, rerr = exitViolation, fmt.Errorf("expected %s, got %s", *expect, c.Verdict)
			vc.Status, vc.Message = report.Failed, rerr.Error()
		}
		suite.Cases = append([]report.Case{vc}, suite.Cases[len(suite.Cases)-1])
	}
	if failed := injectionErrors(res.Applied); failed > 0 && code == exitOK {
		// A clean outcome means little if the planned faults never happened.
		code, rerr = exitError, fmt.Errorf("%d fault commands failed: the cluster did not see the planned schedule (see report)", failed)
	}
	return emitCI(*ci, code, rerr, suite)
}

// addWitnesses attaches minimal witnesses and diagnoses to violations. A
// failure here only costs readability, never the verdict.
func addWitnesses(m model.Model, ops []history.Op, check *checker.Report) {
	_ = checker.AddWitnesses(m, ops, check, checker.WitnessOptions{})
}

func injectionErrors(applied []report.Applied) int {
	n := 0
	for _, a := range applied {
		n += len(a.Errors)
	}
	return n
}
