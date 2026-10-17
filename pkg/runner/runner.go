package runner

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/internal/rng"
	"github.com/kemalhafizh/strife/internal/stats"
	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/client"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/probe"
	"github.com/kemalhafizh/strife/pkg/report"
	"github.com/kemalhafizh/strife/pkg/transport"
	"github.com/kemalhafizh/strife/pkg/workload"
)

// Options control a live run.
type Options struct {
	// Transport overrides the configured transport (tests, dry runs).
	Transport transport.Transport
	// Clients overrides the configured client factory.
	Clients client.Factory
	// Logf receives progress messages.
	Logf func(format string, args ...any)
}

// Result is the outcome of a live run.
type Result struct {
	Schedule nemesis.Schedule
	History  []history.Op
	Applied  []report.Applied
	Probes   []probe.Event
	Findings []probe.Finding
	Check    checker.Report
	// Namespace is the key namespace this run used ("" when the caller
	// supplied its own clients).
	Namespace string
}

// Run executes a live test. The fault schedule and every process's sequence
// of operations are fully determined by the seed; what the cluster does with
// them, of course, is not, which is why the history is recorded and checked.
func Run(ctx context.Context, cfg *Config, opts Options) (*Result, error) {
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	tr := opts.Transport
	var err error
	if tr == nil {
		if tr, err = transport.New(cfg.Transport); err != nil {
			return nil, err
		}
	}
	if cfg, err = ResolveHost(ctx, cfg, tr); err != nil {
		return nil, err
	}
	factory := opts.Clients
	namespace := ""
	if factory == nil {
		namespace = Namespace(cfg, time.Now())
		var err error
		if factory, err = client.NewFactory(cfg.DB.Config, namespace); err != nil {
			return nil, err
		}
	}
	env := cfg.Env()
	sched := cfg.Schedule()
	inj := &Injector{Transport: tr, Nodes: cfg.Nodes, Env: env}

	logf("setup: preparing %d nodes via %s", len(cfg.Nodes), tr.Name())
	if errs := inj.Exec(ctx, env.Setup()); len(errs) > 0 && len(cfg.Nemesis.Faults) > 0 {
		return nil, fmt.Errorf("setup failed: %v", errs)
	}
	// Whatever happens, leave the cluster healed.
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if errs := inj.Teardown(cctx, cfg.Nemesis.Faults); len(errs) > 0 {
			logf("teardown errors: %v", errs)
		}
	}()

	if pc, ok := tr.(transport.ProbeCollector); ok {
		_, _ = pc.CollectProbes(ctx, cfg.Nodes, true)
	}

	start := time.Now()
	clock := func() int64 { return int64(time.Since(start)) }
	rec := history.NewRecorder(clock)
	duration := time.Duration(cfg.Duration)
	runCtx, cancel := context.WithTimeout(ctx, duration+time.Duration(cfg.Workload.Timeout)+5*time.Second)
	defer cancel()

	// Nemesis.
	var applied []report.Applied
	var nemWG sync.WaitGroup
	nemWG.Add(1)
	go func() {
		defer nemWG.Done()
		for _, a := range sched.Timeline() {
			select {
			case <-time.After(time.Until(start.Add(a.At))):
			case <-runCtx.Done():
				return
			}
			// Actual is when dispatch began; Landed is when every node
			// confirmed. Landed - Planned is how late the fault really hit.
			actual := time.Since(start)
			errs := inj.Apply(runCtx, a.Fault, a.Start)
			landed := time.Since(start)
			verb := "inject"
			if !a.Start {
				verb = "heal"
			}
			logf("%7.2fs %s %s (landed %v after plan)", actual.Seconds(), verb, a.Fault.Describe(len(cfg.Nodes)), stats.Round(landed-a.At))
			applied = append(applied, report.Applied{
				FaultID: a.Fault.ID, Kind: a.Fault.Kind, Start: a.Start,
				Planned: a.At, Actual: actual, Jitter: absDuration(actual - a.At),
				Landed: landed, Latency: landed - a.At,
				Method: inj.Method(), Errors: errs,
			})
		}
	}()

	// Workload: one goroutine per logical client slot.
	var plug *workload.Plugin
	if cfg.Workload.Plugin != "" {
		var err error
		plug, err = workload.Load(cfg.Workload.Plugin)
		if err != nil {
			return nil, err
		}
		logf("loaded workload plugin %q (%s, sha256=%s)", plug.Name, plug.Source, plug.SHA256[:12])
	}
	root := rng.New(cfg.Seed)
	var wg sync.WaitGroup
	for i := 0; i < cfg.Workload.Concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			worker(runCtx, cfg, i, root.Fork(fmt.Sprintf("workload/%d", i)), plug, factory, rec, start.Add(duration), logf)
		}(i)
	}
	wg.Wait()
	nemWG.Wait()

	if t := report.InjectionTiming(applied); t.Count > 0 {
		logf("fault timing: %d transitions landed p50=%v p95=%v max=%v after plan", t.Count, stats.Round(t.P50), stats.Round(t.P95), stats.Round(t.Max))
	}

	var probes []probe.Event
	if pc, ok := tr.(transport.ProbeCollector); ok {
		pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
		probes, _ = pc.CollectProbes(pctx, cfg.Nodes, false)
		pcancel()
		if len(probes) > 0 {
			logf("collected %d server-side probe events", len(probes))
		}
	}

	ops := rec.Ops()
	logf("checking %d events", len(ops))
	rep, err := checker.Check(cfg.Model(), ops, checker.Options{MaxSteps: cfg.Checker.MaxSteps, Timeout: time.Duration(cfg.Checker.Timeout)})
	if err != nil {
		return nil, err
	}
	findings := probe.Correlate(ops, rep, sched, probes)
	return &Result{
		Schedule:  sched,
		History:   ops,
		Applied:   applied,
		Probes:    probes,
		Findings:  findings,
		Check:     rep,
		Namespace: namespace,
	}, nil
}

// ResolveHost replaces "auto" cgroups and io_device with what the transport
// discovers on the host. It returns a copy: the caller's config is untouched.
func ResolveHost(ctx context.Context, cfg *Config, tr transport.Transport) (*Config, error) {
	needs := cfg.Nemesis.IODevice == transport.Auto
	for _, n := range cfg.Nodes {
		needs = needs || n.Cgroup == transport.Auto
	}
	if !needs {
		return cfg, nil
	}
	hr, ok := tr.(transport.HostResolver)
	if !ok {
		return nil, fmt.Errorf("\"auto\" cgroups and io_device need the docker transport, not %s", tr.Name())
	}
	c := *cfg
	c.Nodes = append([]transport.Node(nil), cfg.Nodes...)
	for i, n := range c.Nodes {
		if n.Cgroup != transport.Auto {
			continue
		}
		cg, err := hr.ResolveCgroup(ctx, n)
		if err != nil {
			return nil, fmt.Errorf("node %s: resolving its cgroup: %w", n.Name, err)
		}
		c.Nodes[i].Cgroup = cg
	}
	if c.Nemesis.IODevice == transport.Auto {
		dev, err := hr.ResolveIODevice(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolving nemesis.io_device: %w", err)
		}
		c.Nemesis.IODevice = dev
	}
	return &c, c.Env().Validate(c.Nemesis.Faults)
}

// Namespace is the per-run key namespace: the checker assumes every key starts
// absent, so a run must never observe values left over by an earlier run.
func Namespace(cfg *Config, now time.Time) string {
	return fmt.Sprintf("%s-%d-%s", cfg.Name, cfg.Seed, strconv.FormatInt(now.UnixNano(), 36))
}

func worker(ctx context.Context, cfg *Config, slot int, r *rng.Rand, plug *workload.Plugin, factory client.Factory, rec *history.Recorder, until time.Time, logf func(string, ...any)) {
	w := cfg.Workload
	node := slot % len(cfg.Nodes)
	c, err := factory(node)
	if err != nil {
		logf("client %d: %v", slot, err)
		return
	}
	defer func() { _ = c.Close() }()
	process := slot
	step := 0
	var gen *appendGen
	if w.Model == "append" {
		gen = &appendGen{worker: slot, keys: w.Keys, perWorker: max(1, w.WritesPerKey/w.Concurrency), writes: make([]int, w.Keys)}
	}
	for time.Now().Before(until) && ctx.Err() == nil {
		var op history.Op
		if gen != nil {
			op = gen.op(r, w.TxnLen, w.ReadRatio)
			op.Process = process
		} else if plug != nil {
			genOp, err := plug.NextOp(process, step, w.Keys, w.Values, r)
			step++
			if err != nil {
				logf("client %d: workload plugin: %v", slot, err)
				return
			}
			op = genOp
			if !strings.HasPrefix(op.Key, "k") {
				op.Key = fmt.Sprintf("k%d", step%w.Keys)
			}
			switch op.F {
			case history.FRead:
				op.Value, op.Old, op.Txn = nil, nil, nil
			case history.FCas:
				if op.Old == nil {
					op.Old = history.Int(0)
				}
				if op.Value == nil {
					op.Value = history.Int(0)
				}
			default:
				op.F = history.FWrite
				if op.Value == nil {
					op.Value = history.Int(0)
				}
				op.Old, op.Txn = nil, nil
			}
		} else {
			op = history.Op{Process: process, Type: history.Invoke, Key: fmt.Sprintf("k%d", r.Intn(w.Keys))}
			switch x := r.Float64(); {
			case x < w.ReadRatio:
				op.F = history.FRead
			case x < w.ReadRatio+w.CasRatio:
				op.F, op.Old, op.Value = history.FCas, history.Int(r.Int64n(int64(w.Values))), history.Int(r.Int64n(int64(w.Values)))
			default:
				op.F, op.Value = history.FWrite, history.Int(r.Int64n(int64(w.Values)))
			}
		}
		inv := rec.Record(op)
		octx, cancel := context.WithTimeout(ctx, time.Duration(w.Timeout))
		out := c.Invoke(octx, inv)
		cancel()
		out.Process = process
		out.Node = cfg.Nodes[node].Name
		rec.Record(out)
		if out.Type == history.Info {
			// The op may still be in flight: continue as a new process with
			// a fresh connection, exactly like Jepsen.
			process += w.Concurrency
			_ = c.Close()
			if c, err = factory(node); err != nil {
				logf("client %d: reconnect: %v", slot, err)
				return
			}
		}
		if w.Think > 0 {
			select {
			case <-time.After(time.Duration(r.Int64n(int64(w.Think)))):
			case <-ctx.Done():
			}
		}
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// appendGen generates list-append transactions for one worker. Values are
// unique across the run (the worker index is part of each value), and keys
// rotate deterministically: after perWorker of its own appends to a key slot
// the worker moves to the slot's next generation, so each key collects about
// writes_per_key appends in total and lists stay short.
type appendGen struct {
	worker, keys, perWorker int
	writes                  []int // this worker's appends per key slot
	next                    int64
}

func (g *appendGen) key(slot int) string {
	return fmt.Sprintf("a%d.%d", slot, g.writes[slot]/g.perWorker)
}

func (g *appendGen) op(r *rng.Rand, txnLen int, readRatio float64) history.Op {
	n := 1 + r.Intn(txnLen)
	micro := make([]history.MicroOp, 0, n)
	for i := 0; i < n; i++ {
		slot := r.Intn(g.keys)
		if r.Float64() < readRatio {
			micro = append(micro, history.MicroOp{F: history.MicroRead, Key: g.key(slot)})
			continue
		}
		g.next++
		micro = append(micro, history.MicroOp{F: history.MicroAppend, Key: g.key(slot), Value: history.Int(int64(g.worker+1)*1_000_000_000 + g.next)})
		g.writes[slot]++
	}
	return history.Op{Type: history.Invoke, F: history.FTxn, Txn: micro}
}
