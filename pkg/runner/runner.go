package runner

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	// Schedule replaces the seeded fault schedule, to replay a recorded or
	// shrunk one (it must be for the same number of nodes).
	Schedule *nemesis.Schedule
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
	if cfg, err = ResolveNodes(ctx, cfg, tr); err != nil {
		return nil, err
	}
	if cfg, err = ResolveHost(ctx, cfg, tr); err != nil {
		return nil, err
	}
	factory := opts.Clients
	namespace := ""
	if factory == nil {
		namespace = Namespace(cfg, time.Now())
		var err error
		dbc := cfg.DB.Config
		dbc.Isolation = cfg.Workload.Isolation
		if factory, err = client.NewFactory(dbc, namespace); err != nil {
			return nil, err
		}
	}
	env := cfg.Env()
	sched := cfg.Schedule()
	if opts.Schedule != nil {
		if opts.Schedule.Nodes != 0 && opts.Schedule.Nodes != len(cfg.Nodes) {
			return nil, fmt.Errorf("the schedule is for %d nodes, the cluster has %d", opts.Schedule.Nodes, len(cfg.Nodes))
		}
		sched = *opts.Schedule
	}
	inj := &Injector{Transport: tr, Nodes: cfg.Nodes, Env: env}

	logf("setup: preparing %d nodes via %s", len(cfg.Nodes), tr.Name())
	if errs := inj.Exec(ctx, env.Setup(cfg.Nemesis.Faults)); len(errs) > 0 && len(cfg.Nemesis.Faults) > 0 {
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
	var rot *keyRotation
	if cfg.Workload.Model == "append" {
		rot = &keyRotation{perKey: int64(cfg.Workload.WritesPerKey), writes: make([]atomic.Int64, cfg.Workload.Keys)}
	}
	var wg sync.WaitGroup
	for i := 0; i < cfg.Workload.Concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			worker(runCtx, cfg, i, root.Fork(fmt.Sprintf("workload/%d", i)), plug, rot, factory, rec, start.Add(duration), logf)
		}(i)
	}
	wg.Wait()
	nemWG.Wait()
	if cfg.Workload.Model == "append" {
		// Heal, let the cluster settle, then read every key once more: an
		// acknowledged append that no read ever saw is otherwise invisible.
		hctx, hcancel := context.WithTimeout(ctx, time.Minute)
		if errs := inj.Teardown(hctx, cfg.Nemesis.Faults); len(errs) > 0 {
			logf("healing before final reads: %v", errs)
		}
		hcancel()
		finalReads(ctx, cfg, factory, rec, logf)
	}

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
	if rep.Stats.OK == 0 && rep.Verdict == checker.Linearizable {
		// Nothing succeeded, so nothing was tested: a pass would be a lie.
		rep.Verdict = checker.Unknown
		for i := range rep.Keys {
			rep.Keys[i].Verdict, rep.Keys[i].Reason = checker.Unknown, ReasonNoSuccess
		}
		logf("no operation succeeded: the verdict is unknown (see the errors in history.jsonl)")
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

// ReasonNoSuccess explains an unknown verdict for a run in which no
// operation succeeded.
const ReasonNoSuccess = "no operation succeeded"

// ResolveNodes replaces "auto" node IPs with what the transport discovers
// (pod IPs on Kubernetes) and expands db.endpoints entries containing {ip}
// into one endpoint per node. It returns a copy: the caller's config is
// untouched.
func ResolveNodes(ctx context.Context, cfg *Config, tr transport.Transport) (*Config, error) {
	c := *cfg
	c.Nodes = append([]transport.Node(nil), cfg.Nodes...)
	for i, n := range c.Nodes {
		if n.IP != transport.Auto {
			continue
		}
		ir, ok := tr.(transport.IPResolver)
		if !ok {
			return nil, fmt.Errorf("node %s: \"ip\": \"auto\" needs the kubernetes transport, not %s", n.Name, tr.Name())
		}
		ip, err := ir.ResolveIP(ctx, n)
		if err != nil {
			return nil, fmt.Errorf("node %s: %w", n.Name, err)
		}
		c.Nodes[i].IP = ip
	}
	c.DB.Config.Endpoints = ExpandEndpoints(cfg.DB.Config.Endpoints, c.Nodes)
	return &c, nil
}

// ExpandEndpoints turns each endpoint containing {ip} into one endpoint per
// node, in node order, so a client endpoint follows the node's address.
func ExpandEndpoints(endpoints []string, nodes []transport.Node) []string {
	var out []string
	for _, e := range endpoints {
		if !strings.Contains(e, "{ip}") {
			out = append(out, e)
			continue
		}
		for _, n := range nodes {
			out = append(out, strings.ReplaceAll(e, "{ip}", n.IP))
		}
	}
	return out
}

// ResolveHost replaces "auto" cgroups and io_device with what the transport
// discovers on the host. It returns a copy: the caller's config is untouched.
func ResolveHost(ctx context.Context, cfg *Config, tr transport.Transport) (*Config, error) {
	cgroupFaults := slices.Contains(cfg.Nemesis.Faults, nemesis.Freeze) || slices.Contains(cfg.Nemesis.Faults, nemesis.IOStall)
	needs := cfg.Nemesis.IODevice == transport.Auto
	for _, n := range cfg.Nodes {
		needs = needs || n.Cgroup == transport.Auto
	}
	if !needs || !cgroupFaults {
		return cfg, nil // nothing will use them
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

func worker(ctx context.Context, cfg *Config, slot int, r *rng.Rand, plug *workload.Plugin, rot *keyRotation, factory client.Factory, rec *history.Recorder, until time.Time, logf func(string, ...any)) {
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
	if rot != nil {
		gen = &appendGen{worker: slot, keys: rot}
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
		if out.Node == "" { // clients that route requests report the server
			out.Node = cfg.Nodes[node].Name
		}
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

// keyRotation is shared by every worker of an append run: like Elle, all
// workers contend on the same few active keys, and a key slot moves to its
// next generation once it has collected writes_per_key appends in total, so
// lists stay short. (Contention is the point: per-worker keys would never
// conflict.) The keys an operation picks therefore depend on global
// progress, while each worker's random choices still come from its seed.
type keyRotation struct {
	perKey int64
	writes []atomic.Int64
}

func (k *keyRotation) key(slot int) string {
	return fmt.Sprintf("a%d.%d", slot, k.writes[slot].Load()/k.perKey)
}

// appendGen generates list-append transactions for one worker. Values are
// unique across the run: the worker index is part of each value.
type appendGen struct {
	worker int
	keys   *keyRotation
	next   int64
}

func (g *appendGen) op(r *rng.Rand, txnLen int, readRatio float64) history.Op {
	n := 1 + r.Intn(txnLen)
	micro := make([]history.MicroOp, 0, n)
	for i := 0; i < n; i++ {
		slot := r.Intn(len(g.keys.writes))
		key := g.keys.key(slot)
		if r.Float64() < readRatio {
			micro = append(micro, history.MicroOp{F: history.MicroRead, Key: key})
			continue
		}
		g.next++
		micro = append(micro, history.MicroOp{F: history.MicroAppend, Key: key, Value: history.Int(int64(g.worker+1)*1_000_000_000 + g.next)})
		g.keys.writes[slot].Add(1)
	}
	return history.Op{Type: history.Invoke, F: history.FTxn, Txn: micro}
}

// finalReads reads every key the append workload touched, retrying on other
// nodes until each read succeeds or attempts run out.
func finalReads(ctx context.Context, cfg *Config, factory client.Factory, rec *history.Recorder, logf func(string, ...any)) {
	keys := map[string]bool{}
	for _, op := range rec.Ops() {
		for _, m := range op.Txn {
			keys[m.Key] = true
		}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	select {
	case <-time.After(time.Duration(cfg.Workload.Timeout)):
	case <-ctx.Done():
		return
	}
	process := 1 << 30 // far from workload processes
	clients := map[int]client.Client{}
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}()
	missing := 0
	for _, k := range sorted {
		ok := false
		for attempt := 0; attempt < 3*len(cfg.Nodes) && !ok && ctx.Err() == nil; attempt++ {
			node := attempt % len(cfg.Nodes)
			c, found := clients[node]
			if !found {
				var err error
				if c, err = factory(node); err != nil {
					continue
				}
				clients[node] = c
			}
			op := rec.Record(history.Op{Process: process, Type: history.Invoke, F: history.FTxn, Txn: []history.MicroOp{{F: history.MicroRead, Key: k}}})
			octx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Workload.Timeout))
			out := c.Invoke(octx, op)
			cancel()
			out.Process = process
			if out.Node == "" {
				out.Node = cfg.Nodes[node].Name
			}
			rec.Record(out)
			process++
			if ok = out.Type == history.OK; !ok {
				// A failed client may hold a broken connection.
				_ = c.Close()
				delete(clients, node)
			}
		}
		if !ok {
			missing++
		}
	}
	logf("final reads: %d keys, %d could not be read", len(sorted), missing)
}
