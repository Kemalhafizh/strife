package runner

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/internal/rng"
	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/client"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/report"
	"github.com/kemalhafizh/strife/pkg/transport"
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
	if tr == nil {
		var err error
		if tr, err = transport.New(cfg.Transport); err != nil {
			return nil, err
		}
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
	exec := func(ctx context.Context, cmds []nemesis.Command) []string {
		var mu sync.Mutex
		var errs []string
		var wg sync.WaitGroup
		for _, c := range cmds {
			wg.Add(1)
			go func(c nemesis.Command) {
				defer wg.Done()
				n := cfg.Nodes[c.Node]
				cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				var err error
				if c.Host {
					_, err = tr.HostExec(cctx, n, c.Script)
				} else {
					_, err = tr.Exec(cctx, n, c.Script)
				}
				if err != nil {
					mu.Lock()
					errs = append(errs, fmt.Sprintf("%s: %v", n.Name, err))
					mu.Unlock()
				}
			}(c)
		}
		wg.Wait()
		return errs
	}

	logf("setup: preparing %d nodes via %s", len(cfg.Nodes), tr.Name())
	if errs := exec(ctx, env.Setup()); len(errs) > 0 && len(cfg.Nemesis.Faults) > 0 {
		return nil, fmt.Errorf("setup failed: %v", errs)
	}
	// Whatever happens, leave the cluster healed.
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if errs := exec(cctx, env.Teardown(cfg.Nemesis.Faults)); len(errs) > 0 {
			logf("teardown errors: %v", errs)
		}
	}()

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
			cmds := env.Inject(a.Fault)
			verb := "inject"
			if !a.Start {
				cmds, verb = env.Heal(a.Fault), "heal"
			}
			logf("%7.2fs %s %s", time.Since(start).Seconds(), verb, a.Fault.Describe(len(cfg.Nodes)))
			errs := exec(runCtx, cmds)
			applied = append(applied, report.Applied{
				FaultID: a.Fault.ID, Kind: a.Fault.Kind, Start: a.Start,
				Planned: a.At, Actual: time.Since(start), Errors: errs,
			})
		}
	}()

	// Workload: one goroutine per logical client slot.
	root := rng.New(cfg.Seed)
	var wg sync.WaitGroup
	for i := 0; i < cfg.Workload.Concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			worker(runCtx, cfg, i, root.Fork(fmt.Sprintf("workload/%d", i)), factory, rec, start.Add(duration), logf)
		}(i)
	}
	wg.Wait()
	nemWG.Wait()

	ops := rec.Ops()
	logf("checking %d events", len(ops))
	rep, err := checker.Check(model.KV{}, ops, checker.Options{MaxSteps: cfg.Checker.MaxSteps, Timeout: time.Duration(cfg.Checker.Timeout)})
	if err != nil {
		return nil, err
	}
	return &Result{Schedule: sched, History: ops, Applied: applied, Check: rep, Namespace: namespace}, nil
}

// Namespace is the per-run key namespace: the checker assumes every key starts
// absent, so a run must never observe values left over by an earlier run.
func Namespace(cfg *Config, now time.Time) string {
	return fmt.Sprintf("%s-%d-%s", cfg.Name, cfg.Seed, strconv.FormatInt(now.UnixNano(), 36))
}

func worker(ctx context.Context, cfg *Config, slot int, r *rng.Rand, factory client.Factory, rec *history.Recorder, until time.Time, logf func(string, ...any)) {
	w := cfg.Workload
	node := slot % len(cfg.Nodes)
	c, err := factory(node)
	if err != nil {
		logf("client %d: %v", slot, err)
		return
	}
	defer func() { _ = c.Close() }()
	process := slot
	for time.Now().Before(until) && ctx.Err() == nil {
		op := history.Op{Process: process, Type: history.Invoke, Key: fmt.Sprintf("k%d", r.Intn(w.Keys))}
		switch x := r.Float64(); {
		case x < w.ReadRatio:
			op.F = history.FRead
		case x < w.ReadRatio+w.CasRatio:
			op.F, op.Old, op.Value = history.FCas, history.Int(r.Int64n(int64(w.Values))), history.Int(r.Int64n(int64(w.Values)))
		default:
			op.F, op.Value = history.FWrite, history.Int(r.Int64n(int64(w.Values)))
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
