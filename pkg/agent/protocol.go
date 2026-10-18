package agent

import (
	"time"

	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/probe"
)

// DefaultPort is the default TCP listening port for strife-agent.
const DefaultPort = 9292

// NetlinkEngine abstracts kernel netlink and traffic control operations.
type NetlinkEngine interface {
	Available() bool
	SetNetem(iface string, delay, jitter time.Duration, loss float64) error
	DelNetem(iface string) error
}

// Action represents an operation requested from strife-agent.
type Action string

const (
	ActionInject   Action = "inject"
	ActionHeal     Action = "heal"
	ActionTeardown Action = "teardown"
	ActionExec     Action = "exec"
)

// Capabilities describes the fault injection mechanisms available on this node.
type Capabilities struct {
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Netlink       bool   `json:"netlink"`
	CgroupV2      bool   `json:"cgroup_v2"`
	DirectSignals bool   `json:"direct_signals"`
	NFTables      bool   `json:"nftables"`
	IPTables      bool   `json:"iptables"`
	TC            bool   `json:"tc"`
	// Containers reports that the agent can act on other containers on
	// its node (requests with a Target): it sees their processes.
	Containers bool `json:"containers"`
}

// StatusResponse is returned by GET /status and GET /health.
type StatusResponse struct {
	Node         string       `json:"node"`
	Uptime       int64        `json:"uptime_seconds"`
	ActiveFaults int          `json:"active_faults"`
	Capabilities Capabilities `json:"capabilities"`
}

// ApplyRequest specifies a fault injection or healing operation.
type ApplyRequest struct {
	Action    Action        `json:"action"` // inject, heal, teardown, exec
	FaultID   int           `json:"fault_id,omitempty"`
	Kind      nemesis.Kind  `json:"kind,omitempty"`
	TargetIPs []string      `json:"target_ips,omitempty"`
	Iface     string        `json:"iface,omitempty"`
	Delay     time.Duration `json:"delay,omitempty"`
	Jitter    time.Duration `json:"jitter,omitempty"`
	Loss      float64       `json:"loss,omitempty"`
	Process   string        `json:"process,omitempty"`
	StartCmd  string        `json:"start_cmd,omitempty"`
	Cgroup    string        `json:"cgroup,omitempty"`
	IODevice  string        `json:"io_device,omitempty"`
	Offset    time.Duration `json:"offset,omitempty"`
	Script    string        `json:"script,omitempty"` // fallback shell script
	// Target, when set, is the container the fault acts on; otherwise the
	// agent acts on its own namespaces (an agent inside each container).
	Target *Target `json:"target,omitempty"`
}

// Target names the container a request acts on, for an agent that serves a
// whole node (a Kubernetes DaemonSet) instead of running inside the
// container it injects faults into. The agent then applies network faults
// in the container's network namespace, signals only the container's
// processes, and finds the container's cgroup itself ("cgroup": "auto").
type Target struct {
	// ContainerID is the runtime's ID of the container; a runtime prefix
	// ("containerd://", "docker://", "cri-o://") is accepted.
	ContainerID string `json:"container_id"`
}

// ApplyResponse records the outcome and microsecond timings of an operation.
type ApplyResponse struct {
	Success       bool   `json:"success"`
	Applied       string `json:"applied"`
	Method        string `json:"method"` // "netlink", "cgroup", "syscall", "shell"
	ReceivedAtNs  int64  `json:"received_at_ns"`
	KernelAtNs    int64  `json:"kernel_at_ns"`
	CompletedAtNs int64  `json:"completed_at_ns"`
	DurationNs    int64  `json:"duration_ns"`
	Error         string `json:"error,omitempty"`
}

// ExecRequest specifies an arbitrary shell command to run on the agent host.
type ExecRequest struct {
	Script  string        `json:"script"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

// ExecResponse is the result of running an arbitrary shell command.
type ExecResponse struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
}

// ProbesResponse is returned by GET /probes with server-side diagnostic telemetry.
type ProbesResponse struct {
	Node   string        `json:"node"`
	Events []probe.Event `json:"events"`
}
