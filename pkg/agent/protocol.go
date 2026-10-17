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
