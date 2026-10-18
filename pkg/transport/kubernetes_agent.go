package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/kemalhafizh/strife/internal/stats"
	"github.com/kemalhafizh/strife/pkg/agent"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/probe"
)

// KubernetesAgent drives pods through strife-agent running as a DaemonSet
// (examples/kubernetes/strife-agent.yaml): each fault goes to the agent on
// the pod's node, which applies it in the pod's network namespace, to the
// pod's processes and to its cgroup, in about a millisecond instead of a
// kubectl exec round trip. Commands (setup scripts) still go through
// kubectl; nothing runs on the nodes themselves.
type KubernetesAgent struct {
	*Kubernetes
	Agent KubeAgent

	agent *Agent
	port  int

	mu      sync.Mutex
	targets map[string]kubeTarget // by node name
}

// KubeAgent locates the strife-agent DaemonSet pods. They run with the
// node's network, so a pod's IP is its node's address.
type KubeAgent struct {
	Namespace string `json:"namespace,omitempty"` // default strife-system
	Selector  string `json:"selector,omitempty"`  // default app=strife-agent
}

func (a KubeAgent) namespace() string {
	if a.Namespace == "" {
		return "strife-system"
	}
	return a.Namespace
}

func (a KubeAgent) selector() string {
	if a.Selector == "" {
		return "app=strife-agent"
	}
	return a.Selector
}

// kubeTarget is where faults for one node go: the agent on its pod's node,
// and the pod's container.
type kubeTarget struct {
	addr      string
	container string
}

// NewKubernetesAgent builds the transport; c.Token must be resolved.
func NewKubernetesAgent(c Config) *KubernetesAgent {
	bin := c.Kubectl
	if bin == "" {
		bin = "kubectl"
	}
	ka := KubeAgent{}
	if c.Agent != nil {
		ka = *c.Agent
	}
	port := c.AgentPort
	if port == 0 {
		port = agent.DefaultPort
	}
	return &KubernetesAgent{
		Kubernetes: &Kubernetes{Binary: bin, Namespace: c.Namespace, Context: c.Context, Kubeconfig: c.Kubeconfig, Helper: c.NodeHelper},
		Agent:      ka,
		agent:      NewAgent(c),
		port:       port,
		targets:    map[string]kubeTarget{},
	}
}

// Name implements Transport.
func (k *KubernetesAgent) Name() string { return "kubernetes-agent" }

// target finds n's container and the agent on its node, cached until the
// agent reports the container gone.
func (k *KubernetesAgent) target(ctx context.Context, n Node) (kubeTarget, error) {
	k.mu.Lock()
	t, ok := k.targets[n.Name]
	k.mu.Unlock()
	if ok {
		return t, nil
	}
	_, p := k.pod(n)
	if p == "" {
		return kubeTarget{}, fmt.Errorf("kubernetes-agent transport: node %s has no pod", n.Name)
	}
	status := "{.status.containerStatuses[0].containerID}"
	if n.Container != "" {
		status = fmt.Sprintf(`{.status.containerStatuses[?(@.name=="%s")].containerID}`, n.Container)
	}
	out, err := run(ctx, k.Binary, append(k.base(n), "get", "pod", p, "-o", `jsonpath={.spec.nodeName}{" "}`+status)...)
	if err != nil {
		return kubeTarget{}, err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return kubeTarget{}, fmt.Errorf("kubernetes-agent transport: pod %s is not running yet (%q)", p, strings.TrimSpace(out))
	}
	m := containerID.FindStringSubmatch(fields[1])
	if m == nil {
		return kubeTarget{}, fmt.Errorf("kubernetes-agent transport: pod %s has no running container ID (%q)", p, fields[1])
	}
	args := append(k.global(), "--namespace", k.Agent.namespace(), "get", "pods",
		"--selector", k.Agent.selector(), "--field-selector", "spec.nodeName="+fields[0]+",status.phase=Running",
		"-o", "jsonpath={.items[0].status.podIP}")
	out, err = run(ctx, k.Binary, args...)
	ip := strings.TrimSpace(out)
	if err != nil || net.ParseIP(ip) == nil {
		return kubeTarget{}, fmt.Errorf("kubernetes-agent transport: no running strife-agent (%s in %s) on node %s: %v", k.Agent.selector(), k.Agent.namespace(), fields[0], err)
	}
	t = kubeTarget{addr: net.JoinHostPort(ip, strconv.Itoa(k.port)), container: m[1]}
	k.mu.Lock()
	k.targets[n.Name] = t
	k.mu.Unlock()
	return t, nil
}

// Prepare implements Preparer: it finds every node's container and agent
// up front (two kubectl calls per pod, otherwise paid by the first fault).
func (k *KubernetesAgent) Prepare(ctx context.Context, nodes []Node) error {
	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = k.target(ctx, n)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (k *KubernetesAgent) forget(n Node) {
	k.mu.Lock()
	delete(k.targets, n.Name)
	k.mu.Unlock()
}

// apply sends one transition of f to the agent on n's node. When the agent
// no longer finds the container (Kubernetes replaced it), the container's
// ID is looked up again once.
func (k *KubernetesAgent) apply(ctx context.Context, n Node, f nemesis.Fault, env nemesis.Env, inject bool) (string, error) {
	for attempt := 0; ; attempt++ {
		t, err := k.target(ctx, n)
		if err != nil {
			return "", err
		}
		at := n
		at.AgentAddr = t.addr
		applied, err := k.agent.apply(ctx, at, f, env, inject, &agent.Target{ContainerID: t.container})
		if err != nil && attempt == 0 && errors.Is(err, agent.ErrUnknownContainer) {
			k.forget(n)
			continue
		}
		return applied, err
	}
}

// InjectFault implements FaultApplier.
func (k *KubernetesAgent) InjectFault(ctx context.Context, n Node, f nemesis.Fault, env nemesis.Env) (string, error) {
	return k.apply(ctx, n, f, env, true)
}

// HealFault implements FaultApplier.
func (k *KubernetesAgent) HealFault(ctx context.Context, n Node, f nemesis.Fault, env nemesis.Env) (string, error) {
	return k.apply(ctx, n, f, env, false)
}

// TeardownNode implements FaultApplier. The agent serves the whole node, so
// this heals every fault it applied there.
func (k *KubernetesAgent) TeardownNode(ctx context.Context, n Node) error {
	t, err := k.target(ctx, n)
	if err != nil {
		return err
	}
	return k.agent.clientFor(Node{AgentAddr: t.addr}).Teardown(ctx)
}

// HostExec implements Transport by refusing: the DaemonSet agents act on
// containers only (-require-target) and run no scripts on the nodes. Every
// host fault (freeze, io-stall) goes to them as a structured fault instead.
func (k *KubernetesAgent) HostExec(ctx context.Context, n Node, script string) (string, error) {
	return "", fmt.Errorf("kubernetes-agent transport: no host scripts on node of %s: the strife-agent DaemonSet applies host faults itself and runs no scripts on the node", n.Name)
}

// ResolveCgroup implements HostResolver: the agent finds the container's
// cgroup itself, so "auto" is passed on.
func (k *KubernetesAgent) ResolveCgroup(ctx context.Context, n Node) (string, error) {
	if _, err := k.target(ctx, n); err != nil {
		return "", err
	}
	return Auto, nil
}

// ResolveIODevice implements HostResolver: the agent resolves "auto" to the
// disk under its -io-root.
func (k *KubernetesAgent) ResolveIODevice(ctx context.Context) (string, error) {
	return Auto, nil
}

// LatencySummary implements FaultApplier.
func (k *KubernetesAgent) LatencySummary() stats.Summary { return k.agent.LatencySummary() }

// Collector returns the underlying sample collector.
func (k *KubernetesAgent) Collector() *agent.LatencyCollector { return k.agent.Collector() }

// CollectProbes implements ProbeCollector, asking each node's agent once.
func (k *KubernetesAgent) CollectProbes(ctx context.Context, nodes []Node, reset bool) ([]probe.Event, error) {
	var (
		all      []probe.Event
		firstErr error
		asked    = map[string]bool{}
	)
	for _, n := range nodes {
		t, err := k.target(ctx, n)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if asked[t.addr] {
			continue
		}
		asked[t.addr] = true
		evs, err := k.agent.clientFor(Node{AgentAddr: t.addr}).Probes(ctx, reset)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		all = append(all, evs...)
	}
	return all, firstErr
}
