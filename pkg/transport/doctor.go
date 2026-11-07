package transport

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ToolStatus reports whether a host capability or CLI tool is present.
type ToolStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

// EndpointStatus reports the TCP reachability of a configured database endpoint.
type EndpointStatus struct {
	Endpoint  string `json:"endpoint"`
	Reachable bool   `json:"reachable"`
	LatencyMs int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// DoctorReport summarises the host's chaos-testing capabilities and optional
// cluster endpoint probes.
type DoctorReport struct {
	OS                   string           `json:"os"`
	Arch                 string           `json:"arch"`
	Root                 bool             `json:"root"`
	CgroupV2             bool             `json:"cgroup_v2"`
	Tools                []ToolStatus     `json:"tools"`
	SupportedTransports  []string         `json:"supported_transports"`
	RecommendedTransport string           `json:"recommended_transport"`
	ProxyFaults          []string         `json:"proxy_faults"`
	KernelFaults         []string         `json:"kernel_faults,omitempty"`
	Endpoints            []EndpointStatus `json:"endpoints,omitempty"`
	Notes                []string         `json:"notes,omitempty"`
}

// Inspect probes the current host environment and (optionally) dials the
// supplied database endpoints to verify reachability.
func Inspect(ctx context.Context, endpoints []string) DoctorReport {
	rep := DoctorReport{
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Root:        os.Geteuid() == 0,
		ProxyFaults: []string{"partition", "latency", "loss", "kill", "pause", "freeze", "io-stall"},
	}

	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
			rep.CgroupV2 = true
		}
	}

	checkBin := func(name string, arg string) ToolStatus {
		path, err := exec.LookPath(name)
		if err != nil {
			return ToolStatus{Name: name, Available: false, Detail: "not found in PATH"}
		}
		if arg == "" {
			return ToolStatus{Name: name, Available: true, Detail: path}
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		out, err := run(cctx, name, arg)
		if err != nil {
			return ToolStatus{Name: name, Available: true, Detail: path + " (daemon unreachable or needs privilege)"}
		}
		line := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
		if len(line) > 72 {
			line = line[:72]
		}
		return ToolStatus{Name: name, Available: true, Detail: line}
	}

	dockerSt := checkBin("docker", "version")
	kubectlSt := checkBin("kubectl", "")
	sshSt := checkBin("ssh", "")
	tcSt := checkBin("tc", "-V")
	iptSt := checkBin("iptables", "--version")

	rep.Tools = []ToolStatus{
		dockerSt,
		kubectlSt,
		sshSt,
		tcSt,
		iptSt,
		{
			Name:      "cgroup-v2",
			Available: rep.CgroupV2,
			Detail:    mapBool(rep.CgroupV2, "/sys/fs/cgroup/cgroup.controllers", "unavailable (non-Linux or cgroup v1)"),
		},
		{
			Name:      "proxy-l4",
			Available: true,
			Detail:    "built-in user-space TCP fault proxy (zero root/kernel dependencies)",
		},
	}

	rep.SupportedTransports = []string{"proxy", "dry-run"}
	if dockerSt.Available {
		rep.SupportedTransports = append(rep.SupportedTransports, "docker")
	}
	if kubectlSt.Available {
		rep.SupportedTransports = append(rep.SupportedTransports, "kubernetes")
	}
	if sshSt.Available {
		rep.SupportedTransports = append(rep.SupportedTransports, "ssh")
	}
	if runtime.GOOS == "linux" {
		rep.SupportedTransports = append(rep.SupportedTransports, "agent")
	}

	if runtime.GOOS == "linux" && (tcSt.Available || iptSt.Available || rep.CgroupV2) {
		rep.KernelFaults = []string{"partition", "latency", "loss", "kill", "pause", "clock-skew"}
		if rep.CgroupV2 {
			rep.KernelFaults = append(rep.KernelFaults, "freeze", "io-stall")
		}
	}

	switch {
	case runtime.GOOS == "linux" && rep.Root && rep.CgroupV2:
		rep.RecommendedTransport = "agent"
		rep.Notes = append(rep.Notes, "Linux host with root + cgroup v2 detected: kernel-level netlink/cgroup fault injection (`agent` or `docker`) and user-space TCP proxy (`proxy`) are both fully supported.")
	case dockerSt.Available && !strings.Contains(dockerSt.Detail, "unreachable"):
		rep.RecommendedTransport = "proxy"
		rep.Notes = append(rep.Notes, "Docker is available; use transport `\"proxy\"` for zero-privilege L4 fault injection on any OS (Windows/macOS/Linux), or `\"docker\"` on Linux hosts with root for cgroup/tc faults.")
	default:
		rep.RecommendedTransport = "proxy"
		rep.Notes = append(rep.Notes, "Use transport `\"proxy\"` to inject L4 network partitions, latency, packet loss, connection kills, and stalls in user space without root or Linux kernel dependencies.")
	}

	for _, ep := range endpoints {
		addr := endpointHostPort(ep)
		if addr == "" || strings.Contains(addr, "{ip}") {
			continue
		}
		start := time.Now()
		d := net.Dialer{Timeout: 1500 * time.Millisecond}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			rep.Endpoints = append(rep.Endpoints, EndpointStatus{
				Endpoint:  ep,
				Reachable: false,
				Error:     err.Error(),
			})
			continue
		}
		_ = conn.Close()
		rep.Endpoints = append(rep.Endpoints, EndpointStatus{
			Endpoint:  ep,
			Reachable: true,
			LatencyMs: time.Since(start).Milliseconds(),
		})
	}

	return rep
}

func mapBool(cond bool, t, f string) string {
	if cond {
		return t
	}
	return f
}

func endpointHostPort(ep string) string {
	s := strings.TrimSpace(ep)
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	if idx := strings.IndexByte(s, '/'); idx >= 0 {
		s = s[:idx]
	}
	if idx := strings.LastIndexByte(s, '@'); idx >= 0 {
		s = s[idx+1:]
	}
	if _, _, err := net.SplitHostPort(s); err == nil {
		return s
	}
	return fmt.Sprintf("%s", s)
}
