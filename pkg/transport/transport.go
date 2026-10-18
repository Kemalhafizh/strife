// Package transport executes nemesis commands on real nodes: inside docker
// containers or Kubernetes pods, over SSH, through strife-agent, or nowhere
// at all (dry run).
package transport

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/kemalhafizh/strife/internal/secret"
)

// Node is a machine or container under test.
type Node struct {
	Name      string `json:"name"`
	Host      string `json:"host,omitempty"`       // SSH address; defaults to IP
	IP        string `json:"ip"`                   // address peers use to reach this node; "auto" with kubernetes
	Container string `json:"container,omitempty"`  // docker container name, or the container within the pod
	Pod       string `json:"pod,omitempty"`        // kubernetes pod, "name" or "namespace/name"
	Cgroup    string `json:"cgroup,omitempty"`     // cgroup v2 dir of the db process (on the host)
	AgentAddr string `json:"agent_addr,omitempty"` // strife-agent address (host:port or socket path)
}

// Transport runs shell scripts for a node.
type Transport interface {
	// Exec runs script inside the node (container / remote machine).
	Exec(ctx context.Context, n Node, script string) (string, error)
	// HostExec runs script on the machine hosting the node: the docker host
	// for containers, the node itself for SSH.
	HostExec(ctx context.Context, n Node, script string) (string, error)
	Name() string
}

// Config selects and configures a transport.
type Config struct {
	Type   string `json:"type"` // docker | ssh | agent | kubernetes | kubernetes-agent | dry-run
	Docker string `json:"docker,omitempty"`
	// Kubernetes: the kubectl binary, namespace, context and kubeconfig.
	Kubectl    string      `json:"kubectl,omitempty"`
	Namespace  string      `json:"namespace,omitempty"`
	Context    string      `json:"context,omitempty"`
	Kubeconfig string      `json:"kubeconfig,omitempty"`
	NodeHelper *NodeHelper `json:"node_helper,omitempty"` // privileged per-node pods for host faults
	Agent      *KubeAgent  `json:"agent,omitempty"`       // kubernetes-agent: the strife-agent DaemonSet
	User       string      `json:"user,omitempty"`
	Port       int         `json:"port,omitempty"`
	Key        string      `json:"key,omitempty"`
	Sudo       bool        `json:"sudo,omitempty"`       // ssh: remote sudo; docker: sudo for host commands (cgroup faults)
	AgentPort  int         `json:"agent_port,omitempty"` // strife-agent port, default 9292
	Token      string      `json:"token,omitempty"`      // strife-agent token; "env:NAME" or "file:PATH" keeps it out of the config
}

// New builds the configured transport.
func New(c Config) (Transport, error) {
	switch c.Type {
	case "docker":
		bin := c.Docker
		if bin == "" {
			bin = "docker"
		}
		return &Docker{Binary: bin, Sudo: c.Sudo}, nil
	case "kubernetes", "k8s":
		bin := c.Kubectl
		if bin == "" {
			bin = "kubectl"
		}
		return &Kubernetes{Binary: bin, Namespace: c.Namespace, Context: c.Context, Kubeconfig: c.Kubeconfig, Helper: c.NodeHelper}, nil
	case "kubernetes-agent", "k8s-agent":
		token, err := secret.Resolve(c.Token)
		if err != nil {
			return nil, fmt.Errorf("transport.token: %w", err)
		}
		c.Token = token
		return NewKubernetesAgent(c), nil
	case "ssh":
		return &SSH{User: c.User, Port: c.Port, Key: c.Key, Sudo: c.Sudo}, nil
	case "agent":
		token, err := secret.Resolve(c.Token)
		if err != nil {
			return nil, fmt.Errorf("transport.token: %w", err)
		}
		c.Token = token
		return NewAgent(c), nil
	case "dry-run", "dryrun", "":
		return &DryRun{}, nil
	}
	return nil, fmt.Errorf("transport: unknown type %q (docker, ssh, agent, kubernetes, dry-run)", c.Type)
}

// Quote single-quotes s for a POSIX shell.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	s := strings.TrimSpace(out.String())
	if err != nil {
		return s, fmt.Errorf("%s: %w: %s", name, err, s)
	}
	return s, nil
}

// Docker runs scripts with `docker exec` and host scripts locally, so Strife
// must run on the docker host. Host scripts (the cgroup faults) need root:
// run Strife as root or set Sudo to wrap them in `sudo -n`.
type Docker struct {
	Binary string
	Sudo   bool
}

// Name implements Transport.
func (d *Docker) Name() string { return "docker" }

// Exec implements Transport.
func (d *Docker) Exec(ctx context.Context, n Node, script string) (string, error) {
	if n.Container == "" {
		return "", fmt.Errorf("docker transport: node %s has no container", n.Name)
	}
	return run(ctx, d.Binary, "exec", n.Container, "sh", "-c", script)
}

// HostExec implements Transport.
func (d *Docker) HostExec(ctx context.Context, n Node, script string) (string, error) {
	if d.Sudo {
		return run(ctx, "sudo", "-n", "sh", "-c", script)
	}
	return run(ctx, "sh", "-c", script)
}

// ResolveCgroup implements HostResolver: the cgroup of the container's init
// process, which with Docker's default settings holds every process of the
// container.
func (d *Docker) ResolveCgroup(ctx context.Context, n Node) (string, error) {
	if n.Container == "" {
		return "", fmt.Errorf("docker transport: node %s has no container", n.Name)
	}
	out, err := run(ctx, d.Binary, "inspect", "-f", "{{.State.Pid}}", n.Container)
	if err != nil {
		return "", err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || pid <= 0 {
		return "", fmt.Errorf("docker transport: container %s is not running (pid %q)", n.Container, out)
	}
	return cgroupOfPID("/proc", "/sys/fs/cgroup", pid)
}

// ResolveIODevice implements HostResolver: the disk under Docker's data
// root, where container filesystems and volumes live.
func (d *Docker) ResolveIODevice(ctx context.Context) (string, error) {
	root, err := run(ctx, d.Binary, "info", "-f", "{{.DockerRootDir}}")
	if err != nil {
		return "", err
	}
	dev, err := deviceOf(strings.TrimSpace(root))
	if err != nil {
		return "", err
	}
	return wholeDisk("/sys", dev)
}

// SSH runs scripts through the system ssh client, so keys, agents, jump
// hosts and known_hosts behave exactly as they do for the operator.
type SSH struct {
	User string
	Port int
	Key  string
	Sudo bool
}

// Name implements Transport.
func (s *SSH) Name() string { return "ssh" }

// Args returns the ssh argument vector for running script on n.
func (s *SSH) Args(n Node, script string) []string {
	host := n.Host
	if host == "" {
		host = n.IP
	}
	if s.User != "" {
		host = s.User + "@" + host
	}
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}
	if s.Port != 0 {
		args = append(args, "-p", strconv.Itoa(s.Port))
	}
	if s.Key != "" {
		args = append(args, "-i", s.Key)
	}
	if s.Sudo {
		script = "sudo -n sh -c " + Quote(script)
	}
	return append(args, host, script)
}

// Exec implements Transport.
func (s *SSH) Exec(ctx context.Context, n Node, script string) (string, error) {
	return run(ctx, "ssh", s.Args(n, script)...)
}

// HostExec implements Transport.
func (s *SSH) HostExec(ctx context.Context, n Node, script string) (string, error) {
	return s.Exec(ctx, n, script)
}

// DryRun records commands instead of running them.
type DryRun struct {
	mu  sync.Mutex
	Log []string
}

// Name implements Transport.
func (d *DryRun) Name() string { return "dry-run" }

// Exec implements Transport.
func (d *DryRun) Exec(_ context.Context, n Node, script string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Log = append(d.Log, fmt.Sprintf("[%s] %s", n.Name, script))
	return "", nil
}

// HostExec implements Transport.
func (d *DryRun) HostExec(_ context.Context, n Node, script string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Log = append(d.Log, fmt.Sprintf("[host of %s] %s", n.Name, script))
	return "", nil
}
