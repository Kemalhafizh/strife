package campaign

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/explore"
	"github.com/kemalhafizh/strife/pkg/model"
	"github.com/kemalhafizh/strife/pkg/report"
	"github.com/kemalhafizh/strife/pkg/shrink"
	"github.com/kemalhafizh/strife/pkg/transport"
)

// Options configures a multi-seed campaign execution.
type Options struct {
	BaseConfig       explore.Config
	Seeds            []uint64
	Workers          int
	Commit           string
	Branch           string
	OutDir           string
	BaselinePath     string
	SaveBaselinePath string
	ReplayFailing    bool
	KeepReports      int
	ShrinkFirst      int
	Determinism      bool
}

// ReproduceCommand builds a CLI command string that reproduces a single seed's configuration.
func ReproduceCommand(c explore.Config, planPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "strife sim --system %s --seed %d --nodes %d --clients %d --keys %d --values %d --duration %v --op-timeout %v",
		c.System, c.Seed, c.Nodes, c.Clients, c.Keys, c.Values, c.Duration, c.OpTimeout)
	fmt.Fprintf(&b, " --read-ratio %g --cas-ratio %g --fault-interval %v --fault-length %v",
		c.ReadRatio, c.CasRatio, c.FaultInterval, c.FaultLength)
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

// Run executes a parallel seed campaign, compares results against an optional
// baseline manifest, and emits campaign artifacts.
func Run(ctx context.Context, opts Options) (*Manifest, error) {
	if err := opts.BaseConfig.Validate(); err != nil {
		return nil, err
	}
	if len(opts.Seeds) == 0 {
		return nil, fmt.Errorf("campaign: at least one seed is required")
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}

	var baseline *Manifest
	if opts.BaselinePath != "" {
		b, err := LoadManifest(opts.BaselinePath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) || opts.SaveBaselinePath == "" {
				return nil, err
			}
		} else {
			baseline = b
		}
	}

	seeds := MergeSeeds(opts.Seeds, baseline, opts.ReplayFailing)
	start := time.Now()

	records := make([]SeedRecord, len(seeds))
	fullResults := make([]*explore.Result, len(seeds))

	jobs := make(chan int, len(seeds))
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if ctx.Err() != nil {
					records[idx] = SeedRecord{Seed: seeds[idx], Error: ctx.Err().Error()}
					continue
				}
				cfg := opts.BaseConfig
				cfg.Seed = seeds[idx]

				var res *explore.Result
				var err error
				if opts.Determinism {
					var second *explore.Result
					var ok bool
					res, second, ok, err = explore.Determinism(cfg)
					if err == nil && !ok {
						err = fmt.Errorf("nondeterminism detected on seed %d (trace %s vs %s)",
							cfg.Seed, shortHash(res.TraceHash), shortHash(second.TraceHash))
					}
				} else {
					res, err = explore.Run(cfg)
				}

				rec := SeedRecord{
					Seed:      cfg.Seed,
					Reproduce: ReproduceCommand(cfg, ""),
				}
				if err != nil {
					rec.Error = err.Error()
				} else {
					rec.Verdict = res.Check.Verdict
					rec.Ops = res.Check.Operations
					rec.Faults = len(res.Schedule.Faults)
					rec.Events = res.Events
					rec.TraceHash = res.TraceHash
					rec.HistoryHash = res.HistoryHash
					rec.ScheduleHash = res.ScheduleHash
					rec.ElapsedMs = res.Elapsed.Milliseconds()

					if res.Check.Verdict == checker.Violation {
						_ = checker.AddWitnesses(model.KV{}, res.History, &res.Check, checker.WitnessOptions{})
						for _, k := range res.Check.Failures() {
							if k.Counterexample != nil {
								rec.Explanation = fmt.Sprintf("key %s: %s", k.Key, k.Counterexample.Summary())
								break
							}
						}
					}
				}
				records[idx] = rec
				fullResults[idx] = res
			}
		}()
	}

	for i := range seeds {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	// Sort deterministically by Seed
	type pair struct {
		rec SeedRecord
		res *explore.Result
	}
	pairs := make([]pair, len(seeds))
	for i := range seeds {
		pairs[i] = pair{rec: records[i], res: fullResults[i]}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].rec.Seed < pairs[j].rec.Seed })
	for i := range pairs {
		records[i] = pairs[i].rec
		fullResults[i] = pairs[i].res
	}

	// Write per-seed reports and optional shrunk scenarios for violating seeds
	keptCount := 0
	shrunkCount := 0
	for i := range records {
		if records[i].Verdict != checker.Violation || fullResults[i] == nil {
			continue
		}
		res := fullResults[i]
		if opts.OutDir != "" && (opts.KeepReports <= 0 || keptCount < opts.KeepReports) {
			seedDir := filepath.Join(opts.OutDir, fmt.Sprintf("%s-seed%d", res.Config.System, res.Config.Seed))
			rep, err := report.New(report.KindSimulation, res.Config.System, res.Config.Seed, res.Config, res.Schedule, res.History, res.Check, res.TraceHash)
			if err == nil {
				rep.Events = res.Events
				rep.Reproduce = ReproduceCommand(res.Config, filepath.Join(seedDir, "schedule.json"))
				rep.Fingerprint = report.Fingerprint(rep)
				if _, werr := report.WriteDir(seedDir, rep, res.History); werr == nil {
					records[i].ReportPath = filepath.ToSlash(filepath.Join(fmt.Sprintf("%s-seed%d", res.Config.System, res.Config.Seed), "report.html"))
					keptCount++
				}
			}
		}

		if opts.ShrinkFirst > 0 && shrunkCount < opts.ShrinkFirst {
			sres, serr := shrink.Shrink(ctx, res.Config, shrink.Options{Workers: workers})
			if serr == nil && sres != nil {
				records[i].MinimalFaults = len(sres.Faults())
				shrunkCount++
				if opts.OutDir != "" && sres.Final != nil {
					minDir := filepath.Join(opts.OutDir, fmt.Sprintf("%s-seed%d-min", res.Config.System, res.Config.Seed))
					_ = checker.AddWitnesses(model.KV{}, sres.Final.History, &sres.Final.Check, checker.WitnessOptions{})
					rep, err := report.New(report.KindSimulation, sres.Minimal.System, sres.Minimal.Seed, sres.Minimal, sres.Final.Schedule, sres.Final.History, sres.Final.Check, sres.Final.TraceHash)
					if err == nil {
						rep.Events = sres.Final.Events
						rep.Reproduce = ReproduceCommand(sres.Minimal, filepath.Join(minDir, "schedule.json"))
						rep.Shrink = &report.ShrinkInfo{
							OriginalFaults:   len(sres.OriginalSchedule.Faults),
							MinimalFaults:    len(sres.Faults()),
							OriginalOps:      sres.OriginalOps,
							MinimalOps:       sres.Final.Check.Operations,
							OriginalDuration: sres.Original.Duration,
							MinimalDuration:  sres.Minimal.Duration,
							OriginalClients:  sres.Original.Clients,
							MinimalClients:   sres.Minimal.Clients,
							Runs:             sres.Runs,
						}
						rep.Fingerprint = report.Fingerprint(rep)
						if _, werr := report.WriteDir(minDir, rep, sres.Final.History); werr == nil {
							records[i].ReportPath = filepath.ToSlash(filepath.Join(fmt.Sprintf("%s-seed%d-min", res.Config.System, res.Config.Seed), "report.html"))
							records[i].Reproduce = rep.Reproduce
						}
					}
				}
			}
		}
	}

	m := &Manifest{
		Tool:       "strife",
		Version:    report.Version,
		Commit:     opts.Commit,
		Branch:     opts.Branch,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		System:     opts.BaseConfig.System,
		Faults:     opts.BaseConfig.Faults,
		Config:     opts.BaseConfig,
		TotalSeeds: len(records),
		DurationMs: time.Since(start).Milliseconds(),
		Seeds:      records,
	}

	for _, r := range records {
		m.TotalEvents += r.Events
		m.TotalOps += r.Ops
		if r.Error != "" {
			m.Errors++
			continue
		}
		switch r.Verdict {
		case checker.Linearizable:
			m.Passed++
		case checker.Violation:
			m.Violations++
		default:
			m.Unknown++
		}
	}

	m.Fingerprint = m.ComputeFingerprint()
	if len(m.Fingerprint) >= 12 {
		m.CampaignID = fmt.Sprintf("%s-%s", m.System, m.Fingerprint[:12])
	} else {
		m.CampaignID = m.System
	}

	if baseline != nil {
		m.Comparison = Compare(baseline, m)
	}

	if opts.OutDir != "" {
		if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
			return nil, err
		}
		if err := WriteManifest(filepath.Join(opts.OutDir, "campaign.json"), m); err != nil {
			return nil, err
		}
		f, err := os.Create(filepath.Join(opts.OutDir, "campaign.html"))
		if err != nil {
			return nil, err
		}
		herr := WriteHTML(f, m)
		cerr := f.Close()
		if herr != nil {
			return nil, herr
		}
		if cerr != nil {
			return nil, cerr
		}
	}

	if opts.SaveBaselinePath != "" {
		if dir := filepath.Dir(opts.SaveBaselinePath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
		if err := WriteManifest(opts.SaveBaselinePath, m); err != nil {
			return nil, err
		}
	}

	return m, nil
}

func shortHash(h string) string {
	if len(h) > 16 {
		return h[:16]
	}
	return h
}
