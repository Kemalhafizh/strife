package transport

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/agent"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// FaultApplier is an optional fast-path interface implemented by transports
// like strife-agent that can inject structured faults directly into the kernel
// without spawning shell processes.
type FaultApplier interface {
	Transport
	InjectFault(ctx context.Context, n Node, f nemesis.Fault, env nemesis.Env) (string, error)
	HealFault(ctx context.Context, n Node, f nemesis.Fault, env nemesis.Env) (string, error)
	TeardownNode(ctx context.Context, n Node) error
	JitterSummary() agent.JitterSummary
}

// Agent executes commands and faults through per-node strife-agent daemons.
type Agent struct {
	mu        sync.RWMutex
	port      int
	token     string
	clients   map[string]*agent.Client
	collector *agent.JitterCollector
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
		collector: agent.NewJitterCollector(),
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
	c := a.clientFor(n)
	req := agent.ApplyRequest{
		Action:   agent.ActionInject,
		FaultID:  f.ID,
		Kind:     f.Kind,
		Iface:    env.Interface,
		Delay:    f.Delay,
		Jitter:   f.Jitter,
		Loss:     f.Loss,
		Process:  env.Process,
		StartCmd: env.Start,
		Cgroup:   n.Cgroup,
		IODevice: env.IODevice,
		Offset:   f.Offset,
	}

	if f.Kind == nemesis.Partition {
		// Determine which partition side n belongs to:
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
		var targetsToDrop []int
		if inSideA {
			targetsToDrop = f.Other(len(env.Nodes))
		} else {
			targetsToDrop = f.Nodes
		}
		for _, o := range targetsToDrop {
			if o >= 0 && o < len(env.Nodes) {
				req.TargetIPs = append(req.TargetIPs, env.Nodes[o].IP)
			}
		}
	}

	t0 := time.Now()
	resp, err := c.Apply(ctx, req)
	duration := time.Since(t0)

	if resp != nil {
		a.collector.Record(agent.JitterSample{
			FaultID:      f.ID,
			Kind:         f.Kind,
			Node:         n.Name,
			Start:        true,
			DispatchedAt: time.Duration(t0.UnixNano()),
			AppliedAt:    time.Duration(resp.KernelAtNs),
			Duration:     duration,
			Method:       resp.Method,
		})
	}

	if err != nil {
		return "", fmt.Errorf("agent inject on %s: %w", n.Name, err)
	}
	return resp.Applied, nil
}

// HealFault restores state for a structured fault directly via strife-agent.
func (a *Agent) HealFault(ctx context.Context, n Node, f nemesis.Fault, env nemesis.Env) (string, error) {
	c := a.clientFor(n)
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
	}

	t0 := time.Now()
	resp, err := c.Apply(ctx, req)
	duration := time.Since(t0)

	if resp != nil {
		a.collector.Record(agent.JitterSample{
			FaultID:      f.ID,
			Kind:         f.Kind,
			Node:         n.Name,
			Start:        false,
			DispatchedAt: time.Duration(t0.UnixNano()),
			AppliedAt:    time.Duration(resp.KernelAtNs),
			Duration:     duration,
			Method:       resp.Method,
		})
	}

	if err != nil {
		return "", fmt.Errorf("agent heal on %s: %w", n.Name, err)
	}
	return resp.Applied, nil
}

// TeardownNode resets all faults on node n.
func (a *Agent) TeardownNode(ctx context.Context, n Node) error {
	c := a.clientFor(n)
	return c.Teardown(ctx)
}

// JitterSummary returns aggregated jitter metrics recorded by this transport.
func (a *Agent) JitterSummary() agent.JitterSummary {
	return a.collector.Summary()
}

// Collector returns the underlying JitterCollector.
func (a *Agent) Collector() *agent.JitterCollector {
	return a.collector
}
