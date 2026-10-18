package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
)

// Kubernetes runs scripts in pods with `kubectl exec`, so Strife needs only a
// kubeconfig, never SSH access to the nodes. Like containers, pods share the
// node's kernel, so clock-skew (which would skew the whole node) is refused.
//
// Host faults (freeze, io-stall) act on the node's cgroups. They run in a
// privileged node helper: a DaemonSet pod on every node with the node's
// cgroup filesystem mounted (examples/kubernetes/node-helper.yaml). Without
// a helper configured, host commands fail with ErrNoHost.
type Kubernetes struct {
	Binary     string
	Namespace  string
	Context    string
	Kubeconfig string
	Helper     *NodeHelper

	mu      sync.Mutex
	nodeOf  map[string]string // pod → node
	helpers map[string]string // node → helper pod
}

// NodeHelper locates the privileged per-node pods that run host commands.
type NodeHelper struct {
	Namespace string `json:"namespace"`           // e.g. strife-system
	Selector  string `json:"selector"`            // label selector, e.g. app=strife-node
	Container string `json:"container,omitempty"` // container in the helper pod
	// CgroupRoot is where the helper sees the node's cgroup v2 filesystem
	// (default /host/sys/fs/cgroup); DataDir is a directory on the disk
	// that holds container data (default /host/var/lib/containerd).
	CgroupRoot string `json:"cgroup_root,omitempty"`
	DataDir    string `json:"data_dir,omitempty"`
}

func (h *NodeHelper) cgroupRoot() string {
	if h.CgroupRoot == "" {
		return "/host/sys/fs/cgroup"
	}
	return h.CgroupRoot
}

func (h *NodeHelper) dataDir() string {
	if h.DataDir == "" {
		return "/host/var/lib/containerd"
	}
	return h.DataDir
}

// ErrNoHost is returned for host commands on transports without host access.
var ErrNoHost = errors.New("this transport cannot run commands on the machine hosting a node")

// Name implements Transport.
func (k *Kubernetes) Name() string { return "kubernetes" }

func (k *Kubernetes) base(n Node) []string {
	args := k.global()
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

// HostExec implements Transport: script runs in the node helper on the node
// that hosts n's pod.
func (k *Kubernetes) HostExec(ctx context.Context, n Node, script string) (string, error) {
	if k.Helper == nil {
		return "", fmt.Errorf("kubernetes transport: %w (configure transport.node_helper for freeze and io-stall)", ErrNoHost)
	}
	node, err := k.node(ctx, n)
	if err != nil {
		return "", err
	}
	helper, err := k.helper(ctx, node)
	if err != nil {
		return "", err
	}
	return run(ctx, k.Binary, k.helperExecArgs(helper, script)...)
}

func (k *Kubernetes) global() []string {
	var args []string
	if k.Kubeconfig != "" {
		args = append(args, "--kubeconfig", k.Kubeconfig)
	}
	if k.Context != "" {
		args = append(args, "--context", k.Context)
	}
	return args
}

func (k *Kubernetes) helperExecArgs(pod, script string) []string {
	args := append(k.global(), "--namespace", k.Helper.Namespace, "exec", pod)
	if k.Helper.Container != "" {
		args = append(args, "--container", k.Helper.Container)
	}
	return append(args, "--", "sh", "-c", script)
}

// node returns the node that runs n's pod, cached for the run.
func (k *Kubernetes) node(ctx context.Context, n Node) (string, error) {
	k.mu.Lock()
	cached, ok := k.nodeOf[n.Pod]
	k.mu.Unlock()
	if ok {
		return cached, nil
	}
	_, p := k.pod(n)
	out, err := run(ctx, k.Binary, append(k.base(n), "get", "pod", p, "-o", "jsonpath={.spec.nodeName}")...)
	if err != nil {
		return "", err
	}
	node := strings.TrimSpace(out)
	if node == "" {
		return "", fmt.Errorf("kubernetes transport: pod %s is not scheduled on a node", p)
	}
	k.mu.Lock()
	if k.nodeOf == nil {
		k.nodeOf = map[string]string{}
	}
	k.nodeOf[n.Pod] = node
	k.mu.Unlock()
	return node, nil
}

// helper returns the running node-helper pod on node, cached for the run.
func (k *Kubernetes) helper(ctx context.Context, node string) (string, error) {
	k.mu.Lock()
	cached, ok := k.helpers[node]
	k.mu.Unlock()
	if ok {
		return cached, nil
	}
	args := append(k.global(), "--namespace", k.Helper.Namespace, "get", "pods",
		"--selector", k.Helper.Selector, "--field-selector", "spec.nodeName="+node+",status.phase=Running",
		"-o", "jsonpath={.items[0].metadata.name}")
	out, err := run(ctx, k.Binary, args...)
	pod := strings.TrimSpace(out)
	if err != nil || pod == "" {
		return "", fmt.Errorf("kubernetes transport: no running node helper (%s in %s) on node %s: %v", k.Helper.Selector, k.Helper.Namespace, node, err)
	}
	k.mu.Lock()
	if k.helpers == nil {
		k.helpers = map[string]string{}
	}
	k.helpers[node] = pod
	k.mu.Unlock()
	return pod, nil
}

var containerID = regexp.MustCompile(`^[a-z0-9-]+://([0-9a-f]{12,})$`)

// ResolveCgroup implements HostResolver: the cgroup of n's container, as the
// node helper sees it, found by the container ID the kubelet reports.
func (k *Kubernetes) ResolveCgroup(ctx context.Context, n Node) (string, error) {
	if k.Helper == nil {
		return "", fmt.Errorf("kubernetes transport: %w (configure transport.node_helper)", ErrNoHost)
	}
	_, p := k.pod(n)
	path := "{.status.containerStatuses[0].containerID}"
	if n.Container != "" {
		path = fmt.Sprintf(`{.status.containerStatuses[?(@.name=="%s")].containerID}`, n.Container)
	}
	out, err := run(ctx, k.Binary, append(k.base(n), "get", "pod", p, "-o", "jsonpath="+path)...)
	if err != nil {
		return "", err
	}
	m := containerID.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return "", fmt.Errorf("kubernetes transport: pod %s has no running container ID (%q)", p, out)
	}
	root := k.Helper.cgroupRoot()
	found, err := k.HostExec(ctx, n, fmt.Sprintf("find %s -maxdepth 8 -type d -name '*%s*' 2>/dev/null | head -n 1", Quote(root), m[1]))
	if err != nil {
		return "", err
	}
	cg := strings.TrimSpace(found)
	if !strings.HasPrefix(cg, root+"/") {
		return "", fmt.Errorf("kubernetes transport: no cgroup for container %.12s under %s on the node", m[1], root)
	}
	return cg, nil
}

// diskScript prints the "major:minor" of the whole disk holding dir: the
// device of the filesystem, or its parent when it is a partition.
const diskScript = `d=$(stat -c %%d %s) || exit 1
maj=$(( (d >> 8) & 0xfff )); min=$(( (d & 0xff) | ((d >> 12) & 0xfff00) ))
p=/sys/dev/block/$maj:$min
[ -e "$p" ] || { echo "$maj:$min is not a block device" >&2; exit 1; }
if [ -e "$p/partition" ]; then cat "$(readlink -f "$p")/../dev"; else echo "$maj:$min"; fi`

// ResolveIODevice implements HostResolver: the disk holding container data,
// read through the node helper of the first node that has one.
func (k *Kubernetes) ResolveIODevice(ctx context.Context) (string, error) {
	if k.Helper == nil {
		return "", fmt.Errorf("kubernetes transport: %w (configure transport.node_helper)", ErrNoHost)
	}
	args := append(k.global(), "--namespace", k.Helper.Namespace, "get", "pods",
		"--selector", k.Helper.Selector, "--field-selector", "status.phase=Running",
		"-o", "jsonpath={.items[0].metadata.name}")
	out, err := run(ctx, k.Binary, args...)
	pod := strings.TrimSpace(out)
	if err != nil || pod == "" {
		return "", fmt.Errorf("kubernetes transport: no running node helper: %v", err)
	}
	dev, err := run(ctx, k.Binary, k.helperExecArgs(pod, fmt.Sprintf(diskScript, Quote(k.Helper.dataDir())))...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(dev), nil
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
