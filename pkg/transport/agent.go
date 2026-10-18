package transport

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/internal/stats"
	"github.com/kemalhafizh/strife/pkg/agent"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/probe"
)

// FaultApplier is an optional fast-path interface implemented by transports
// like strife-agent that can inject structured faults directly into the kernel
// without spawning shell processes.
type FaultApplier interface {
	Transport
	InjectFault(ctx context.Context, n Node, f nemesis.Fault, env nemesis.Env) (string, error)
	HealFault(ctx context.Context, n Node, f nemesis.Fault, env nemesis.Env) (string, error)
	TeardownNode(ctx context.Context, n Node) error
	// LatencySummary summarises the round trip of every transition sent.
	LatencySummary() stats.Summary
}

// Preparer is an optional interface for transports with slow per-node
// lookups (the Kubernetes API): Prepare does them before the first fault,
// so that fault lands on time and a missing node fails the setup.
type Preparer interface {
	Prepare(ctx context.Context, nodes []Node) error
}

// ProbeCollector is an optional interface implemented by transports that can
// harvest server-side diagnostic telemetry (fsync stalls, PSI, OOMs, signals)
// from target cluster nodes.
type ProbeCollector interface {
	Transport
	CollectProbes(ctx context.Context, nodes []Node, reset bool) ([]probe.Event, error)
}

// Agent executes commands and faults through per-node strife-agent daemons.
type Agent struct {
	mu        sync.RWMutex
	port      int
	token     string
	clients   map[string]*agent.Client
	collector *agent.LatencyCollector
}

// NewAgent creates an Agent transport.
func NewAgent(c Config) *Agent {
	port := c.AgentPort
	if port == 0 {
		port = agent.DefaultPort
	}
	return &Agent{
		port:      port,
		token:     c.Token,
		clients:   make(map[string]*agent.Client),
		collector: agent.NewLatencyCollector(),
	}
}

// Name implements Transport.
func (a *Agent) Name() string { return "agent" }

func (a *Agent) clientFor(n Node) *agent.Client {
	addr := n.AgentAddr
	if addr == "" {
		host := n.IP
		if host == "" {
			host = n.Host
		}
		if host == "" {
			host = "127.0.0.1"
		}
		addr = host + ":" + strconv.Itoa(a.port)
	}

	a.mu.RLock()
	c, ok := a.clients[addr]
	a.mu.RUnlock()
	if ok {
		return c
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok = a.clients[addr]; ok {
		return c
	}
	c = agent.NewClient(addr, a.token)
	a.clients[addr] = c
	return c
}

// Exec implements Transport by running the script via the agent daemon.
func (a *Agent) Exec(ctx context.Context, n Node, script string) (string, error) {
	c := a.clientFor(n)
	return c.Exec(ctx, script)
}

// HostExec runs host script locally or via the node's agent if configured.
func (a *Agent) HostExec(ctx context.Context, n Node, script string) (string, error) {
	// For agent transport, host commands run on the host system where runner is executed
	// or fallback to agent if host is remote.
	return run(ctx, "sh", "-c", script)
}

// InjectFault dispatches a structured fault directly to the node's strife-agent.
func (a *Agent) InjectFault(ctx context.Context, n Node, f nemesis.Fault, env nemesis.Env) (string, error) {
	return a.apply(ctx, n, f, env, true, nil)
}

// HealFault restores state for a structured fault directly via strife-agent.
func (a *Agent) HealFault(ctx context.Context, n Node, f nemesis.Fault, env nemesis.Env) (string, error) {
	return a.apply(ctx, n, f, env, false, nil)
}

// apply sends the start (inject) or end of f on n to n's agent; target, when
// set, is the container the agent acts on (an agent serving a whole node).
func (a *Agent) apply(ctx context.Context, n Node, f nemesis.Fault, env nemesis.Env, inject bool, target *agent.Target) (string, error) {
	req := agent.ApplyRequest{
		Action:   agent.ActionHeal,
		FaultID:  f.ID,
		Kind:     f.Kind,
		Iface:    env.Interface,
		Process:  env.Process,
		StartCmd: env.Start,
		Cgroup:   n.Cgroup,
		IODevice: env.IODevice,
		Offset:   f.Offset,
		Target:   target,
	}
	verb := "heal"
	if inject {
		req.Action, verb = agent.ActionInject, "inject"
		req.Delay, req.Jitter, req.Loss = f.Delay, f.Jitter, f.Loss
		if f.Kind == nemesis.Partition {
			req.TargetIPs = partitionPeers(n, f, env)
		}
	}
	resp, err := a.timedApply(ctx, a.clientFor(n), n, f, inject, req)
	if err != nil {
		return "", fmt.Errorf("agent %s on %s: %w", verb, n.Name, err)
	}
	return resp.Applied, nil
}

// partitionPeers returns the addresses n must drop during partition f: the
// nodes on the other side.
func partitionPeers(n Node, f nemesis.Fault, env nemesis.Env) []string {
	nodeIdx := -1
	for idx, target := range env.Nodes {
		if target.Name == n.Name || target.IP == n.IP {
			nodeIdx = idx
			break
		}
	}
	inSideA := false
	for _, idx := range f.Nodes {
		if idx == nodeIdx {
			inSideA = true
			break
		}
	}
	others := f.Nodes
	if inSideA {
		others = f.Other(len(env.Nodes))
	}
	var ips []string
	for _, o := range others {
		if o >= 0 && o < len(env.Nodes) {
			ips = append(ips, env.Nodes[o].IP)
		}
	}
	return ips
}

// timedApply sends one transition and records how long it took.
func (a *Agent) timedApply(ctx context.Context, c *agent.Client, n Node, f nemesis.Fault, start bool, req agent.ApplyRequest) (*agent.ApplyResponse, error) {
	t0 := time.Now()
	resp, err := c.Apply(ctx, req)
	if resp != nil {
		a.collector.Record(agent.LatencySample{
			FaultID:   f.ID,
			Kind:      f.Kind,
			Node:      n.Name,
			Start:     start,
			RoundTrip: time.Since(t0),
			Server:    time.Duration(resp.KernelAtNs - resp.ReceivedAtNs),
			Method:    resp.Method,
		})
	}
	return resp, err
}

// TeardownNode resets all faults on node n.
func (a *Agent) TeardownNode(ctx context.Context, n Node) error {
	c := a.clientFor(n)
	return c.Teardown(ctx)
}

// LatencySummary implements FaultApplier.
func (a *Agent) LatencySummary() stats.Summary { return a.collector.Summary() }

// Collector returns the underlying sample collector.
func (a *Agent) Collector() *agent.LatencyCollector { return a.collector }

// CollectProbes harvests server-side diagnostic probe events from all target nodes.
func (a *Agent) CollectProbes(ctx context.Context, nodes []Node, reset bool) ([]probe.Event, error) {
	var all []probe.Event
	var firstErr error
	for _, n := range nodes {
		c := a.clientFor(n)
		evs, err := c.Probes(ctx, reset)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, ev := range evs {
			if ev.Node == "" {
				ev.Node = n.Name
			}
			all = append(all, ev)
		}
	}
	return all, firstErr
}
