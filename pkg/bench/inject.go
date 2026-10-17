// Package bench measures Strife itself on real infrastructure, so the
// numbers quoted in the documentation can be reproduced by anyone with the
// same setup.
package bench

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/internal/stats"
	"github.com/kemalhafizh/strife/pkg/agent"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/runner"
	"github.com/kemalhafizh/strife/pkg/transport"
)

// InjectConfig describes an injection benchmark.
type InjectConfig struct {
	// Cluster supplies the nodes, the database process (for kill and pause)
	// and the transport settings shared by every transport compared.
	Cluster *runner.Config
	// Transports to compare, e.g. docker and agent.
	Transports []string
	Kinds      []nemesis.Kind
	// Cycles is the number of inject+heal pairs per kind and transport.
	Cycles int
	// Period separates consecutive transitions.
	Period time.Duration
	Logf   func(format string, args ...any)
}

// Row summarises one transport, fault kind and edge (inject or heal).
//
// Latency runs from the planned instant until every involved node confirmed
// the change, i.e. how late the fault really took effect. For iptables, tc,
// signals and cgroup writes the confirming call returns after the kernel
// applied the change; freeze additionally waits for cgroup.events to report
// the cgroup frozen. Wake is the part of it spent before dispatch even began
// (scheduler and timer slack), which no transport can remove.
type Row struct {
	Transport string        `json:"transport"`
	Method    string        `json:"method"`
	Kind      nemesis.Kind  `json:"kind"`
	Edge      string        `json:"edge"`
	Latency   stats.Summary `json:"latency"`
	Wake      stats.Summary `json:"wake"`
	// Agent is, for agent transports, the time spent inside the agents from
	// receiving the request until the kernel call returned (the slowest
	// node per transition). It excludes the trip back to Strife, which
	// latency and loss faults slow down on the interface they impair.
	Agent      *stats.Summary `json:"agent,omitempty"`
	Errors     int            `json:"errors"`
	FirstError string         `json:"first_error,omitempty"`
}

// Host identifies the machine the benchmark ran on.
type Host struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	CPUs      int    `json:"cpus"`
	Kernel    string `json:"kernel,omitempty"`
	CPUModel  string `json:"cpu_model,omitempty"`
	GoVersion string `json:"go_version"`
}

// InjectResult is the outcome of an injection benchmark.
type InjectResult struct {
	Started time.Time     `json:"started"`
	Host    Host          `json:"host"`
	Nodes   int           `json:"nodes"`
	Cycles  int           `json:"cycles"`
	Period  time.Duration `json:"period"`
	Rows    []Row         `json:"rows"`
}

// Fault returns the fault the benchmark injects for kind on cycle i: always
// a single node, rotating through the cluster, with fixed magnitudes.
func Fault(kind nemesis.Kind, i, nodes int) nemesis.Fault {
	f := nemesis.Fault{ID: i, Kind: kind, Nodes: []int{i % nodes}}
	switch kind {
	case nemesis.Latency:
		f.Delay, f.Jitter = 50*time.Millisecond, 10*time.Millisecond
	case nemesis.Loss:
		f.Loss = 0.2
	case nemesis.ClockSkew:
		f.Offset = 5 * time.Second
	}
	return f
}

// Inject runs the benchmark. Faults of different kinds and transports never
// overlap: each kind is fully healed before the next starts.
func Inject(ctx context.Context, c InjectConfig) (*InjectResult, error) {
	logf := c.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if c.Cycles < 1 {
		return nil, fmt.Errorf("bench: cycles must be positive")
	}
	if c.Period <= 0 {
		c.Period = 250 * time.Millisecond
	}
	env := c.Cluster.Env()
	if err := env.Validate(c.Kinds); err != nil {
		return nil, err
	}
	res := &InjectResult{Started: time.Now().UTC(), Host: host(), Nodes: len(c.Cluster.Nodes), Cycles: c.Cycles, Period: c.Period}
	for _, name := range c.Transports {
		tc := c.Cluster.Transport
		tc.Type = name
		tr, err := transport.New(tc)
		if err != nil {
			return nil, err
		}
		inj := &runner.Injector{Transport: tr, Nodes: c.Cluster.Nodes, Env: env, CommandTimeout: 10 * time.Second}
		if errs := inj.Exec(ctx, env.Setup(c.Kinds)); len(errs) > 0 {
			return nil, fmt.Errorf("bench: setup via %s: %s", name, strings.Join(errs, "; "))
		}
		for _, kind := range c.Kinds {
			rows := measure(ctx, inj, name, kind, c.Cycles, c.Period, len(c.Cluster.Nodes))
			for _, r := range rows {
				logf("%-7s %-10s %-6s p50=%-9v p95=%-9v max=%-9v errors=%d", r.Transport, r.Kind, r.Edge,
					stats.Round(r.Latency.P50), stats.Round(r.Latency.P95), stats.Round(r.Latency.Max), r.Errors)
			}
			res.Rows = append(res.Rows, rows...)
			if err := ctx.Err(); err != nil {
				_ = inj.Teardown(context.Background(), c.Kinds)
				return res, err
			}
		}
		tctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		if errs := inj.Teardown(tctx, c.Kinds); len(errs) > 0 {
			logf("teardown via %s: %s", name, strings.Join(errs, "; "))
		}
		cancel()
	}
	return res, nil
}

func measure(ctx context.Context, inj *runner.Injector, name string, kind nemesis.Kind, cycles int, period time.Duration, nodes int) []Row {
	type acc struct {
		lat, wake, agent []time.Duration
		errs             int
		first            string
	}
	col, _ := inj.Transport.(interface {
		Collector() *agent.LatencyCollector
	})
	var edges [2]acc // inject, heal
	t0 := time.Now().Add(period)
	for i := 0; i < cycles && ctx.Err() == nil; i++ {
		f := Fault(kind, i, nodes)
		for e, start := range []bool{true, false} {
			planned := t0.Add(time.Duration(2*i+e) * period)
			select {
			case <-time.After(time.Until(planned)):
			case <-ctx.Done():
			}
			seen := 0
			if col != nil {
				seen = len(col.Collector().Samples())
			}
			dispatched := time.Now()
			errs := inj.Apply(ctx, f, start)
			landed := time.Now()
			a := &edges[e]
			if len(errs) > 0 {
				a.errs++
				if a.first == "" {
					a.first = errs[0]
				}
				continue
			}
			a.lat = append(a.lat, landed.Sub(planned))
			a.wake = append(a.wake, dispatched.Sub(planned))
			if col != nil {
				var slowest time.Duration
				for _, smp := range col.Collector().Samples()[seen:] {
					slowest = max(slowest, smp.Server)
				}
				a.agent = append(a.agent, slowest)
			}
		}
	}
	var rows []Row
	for e, edge := range []string{"inject", "heal"} {
		a := edges[e]
		row := Row{
			Transport: name, Method: inj.Method(), Kind: kind, Edge: edge,
			Latency: stats.Summarize(a.lat), Wake: stats.Summarize(a.wake),
			Errors: a.errs, FirstError: a.first,
		}
		if col != nil {
			sum := stats.Summarize(a.agent)
			row.Agent = &sum
		}
		rows = append(rows, row)
	}
	return rows
}

func host() Host {
	h := Host{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), GoVersion: runtime.Version()}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		h.Kernel = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "model name"); ok {
				h.CPUModel = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), ":"))
				break
			}
		}
	}
	return h
}

// Markdown renders the result as a table, one row per transport, kind and
// edge, followed by the environment needed to interpret it.
func (r *InjectResult) Markdown() string {
	var b strings.Builder
	b.WriteString("| Transport | Fault | Edge | n | P50 | P95 | P99 | Max | Std dev | Agent-side P50 / P95 | Errors |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|\n")
	agentRows := false
	for _, row := range r.Rows {
		l := row.Latency
		side := "–"
		if row.Agent != nil && row.Agent.Count > 0 {
			side = fmt.Sprintf("%v / %v", stats.Round(row.Agent.P50), stats.Round(row.Agent.P95))
			agentRows = true
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %v | %v | %v | %v | %v | %s | %d |\n", row.Method, row.Kind, row.Edge, l.Count,
			stats.Round(l.P50), stats.Round(l.P95), stats.Round(l.P99), stats.Round(l.Max), stats.Round(l.StdDev), side, row.Errors)
	}
	fmt.Fprintf(&b, "\nLatency: from the planned instant until every involved node confirmed the change. %d cycles per row, one transition every %v, %d nodes.\n",
		r.Cycles, r.Period, r.Nodes)
	if agentRows {
		b.WriteString("Agent-side: inside the agent, from receiving the request until the kernel call returned (slowest node). " +
			"The agent's reply leaves through the interface it just impaired, so latency and loss also delay the confirmation, not the fault.\n")
	}
	fmt.Fprintf(&b, "Host: %s/%s, %d CPUs", r.Host.OS, r.Host.Arch, r.Host.CPUs)
	if r.Host.CPUModel != "" {
		fmt.Fprintf(&b, " (%s)", r.Host.CPUModel)
	}
	if r.Host.Kernel != "" {
		fmt.Fprintf(&b, ", kernel %s", r.Host.Kernel)
	}
	fmt.Fprintf(&b, ", %s, %s.\n", r.Host.GoVersion, r.Started.Format("2006-01-02"))
	for _, row := range r.Rows {
		if row.FirstError != "" {
			fmt.Fprintf(&b, "\n- %s %s %s: %s", row.Method, row.Kind, row.Edge, row.FirstError)
		}
	}
	return b.String()
}
