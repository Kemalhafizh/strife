package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Kubernetes runs scripts in pods with `kubectl exec`, so Strife needs only a
// kubeconfig, never SSH access to the nodes. Like containers, pods share the
// node's kernel: host faults (freeze, io-stall) need strife-agent running on
// the node, and clock-skew would skew the whole node.
type Kubernetes struct {
	Binary     string
	Namespace  string
	Context    string
	Kubeconfig string
}

// ErrNoHost is returned for host commands on transports without host access.
var ErrNoHost = errors.New("this transport cannot run commands on the machine hosting a node")

// Name implements Transport.
func (k *Kubernetes) Name() string { return "kubernetes" }

func (k *Kubernetes) base(n Node) []string {
	var args []string
	if k.Kubeconfig != "" {
		args = append(args, "--kubeconfig", k.Kubeconfig)
	}
	if k.Context != "" {
		args = append(args, "--context", k.Context)
	}
	ns, _ := k.pod(n)
	if ns != "" {
		args = append(args, "--namespace", ns)
	}
	return args
}

// pod returns the namespace and pod of n: "ns/pod" or a bare pod name in the
// configured namespace.
func (k *Kubernetes) pod(n Node) (string, string) {
	if ns, p, ok := strings.Cut(n.Pod, "/"); ok {
		return ns, p
	}
	return k.Namespace, n.Pod
}

// ExecArgs returns the kubectl argument vector that runs script in n.
func (k *Kubernetes) ExecArgs(n Node, script string) ([]string, error) {
	_, p := k.pod(n)
	if p == "" {
		return nil, fmt.Errorf("kubernetes transport: node %s has no pod", n.Name)
	}
	args := append(k.base(n), "exec", p)
	if n.Container != "" {
		args = append(args, "--container", n.Container)
	}
	return append(args, "--", "sh", "-c", script), nil
}

// Exec implements Transport.
func (k *Kubernetes) Exec(ctx context.Context, n Node, script string) (string, error) {
	args, err := k.ExecArgs(n, script)
	if err != nil {
		return "", err
	}
	return run(ctx, k.Binary, args...)
}

// HostExec implements Transport. Reaching the node itself would need a
// privileged debug pod per fault; strife-agent as a DaemonSet does it better.
func (k *Kubernetes) HostExec(ctx context.Context, n Node, script string) (string, error) {
	return "", fmt.Errorf("kubernetes transport: %w (run strife-agent on the node for freeze and io-stall)", ErrNoHost)
}

// ResolveIP implements IPResolver: the pod's IP, which survives container
// restarts but not the pod being rescheduled, so it is read at every run.
func (k *Kubernetes) ResolveIP(ctx context.Context, n Node) (string, error) {
	_, p := k.pod(n)
	if p == "" {
		return "", fmt.Errorf("kubernetes transport: node %s has no pod", n.Name)
	}
	out, err := run(ctx, k.Binary, append(k.base(n), "get", "pod", p, "-o", "jsonpath={.status.podIP}")...)
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(out)
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("kubernetes transport: pod %s has no IP yet (%q)", p, ip)
	}
	return ip, nil
}

// IPResolver is implemented by transports that can discover a node's IP, for
// nodes configured with "ip": "auto".
type IPResolver interface {
	ResolveIP(ctx context.Context, n Node) (string, error)
}
