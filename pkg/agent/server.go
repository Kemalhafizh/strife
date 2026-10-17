package agent

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/probe"
)

// ServerConfig configures the strife-agent daemon.
type ServerConfig struct {
	NodeName  string
	Interface string
	Token     string // optional shared secret

	// filters overrides the packet filter backends (tests).
	filters []filterBackend
}

// Server is the strife-agent per-node daemon.
type Server struct {
	cfg       ServerConfig
	startTime time.Time
	httpSrv   *http.Server

	netlink NetlinkEngine
	filter  *FilterEngine
	process *ProcessEngine
	cgroup  *CgroupEngine
	probes  *probe.Collector

	mu           sync.Mutex
	activeFaults map[int]ApplyRequest
}

// NewServer initializes a new strife-agent server.
func NewServer(cfg ServerConfig) *Server {
	if cfg.Interface == "" {
		cfg.Interface = "eth0"
	}
	now := time.Now()
	s := &Server{
		cfg:       cfg,
		startTime: now,
		netlink:   NewNetlinkEngine(),
		filter:    NewFilterEngine(),
		process:   NewProcessEngine(),
		cgroup:    NewCgroupEngine(),
		probes: probe.NewCollector(probe.CollectorConfig{
			Node:  cfg.NodeName,
			Epoch: now,
		}),
		activeFaults: make(map[int]ApplyRequest),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/status", s.handleHealth)
	mux.HandleFunc("/apply", s.handleApply)
	mux.HandleFunc("/exec", s.handleExec)
	mux.HandleFunc("/probes", s.handleProbes)
	mux.HandleFunc("/teardown", s.handleTeardown)
	s.httpSrv = &http.Server{
		Handler:      s.authMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	if cfg.filters != nil {
		s.filter.backends = cfg.filters
	}
	return s
}

// Probes returns the server-side diagnostic probe collector.
func (s *Server) Probes() *probe.Collector {
	return s.probes
}

// Capabilities returns detected system capabilities.
func (s *Server) Capabilities() Capabilities {
	return Capabilities{
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Netlink:       s.netlink.Available(),
		CgroupV2:      s.cgroup.Available(),
		DirectSignals: runtime.GOOS == "linux" || runtime.GOOS == "darwin",
		NFTables:      s.filter.Method() == "filter:nftables",
		IPTables:      s.filter.Method() == "filter:iptables",
		TC:            s.netlink.Available(),
	}
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Token)) != 1 {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Serve accepts incoming connections on listener l.
func (s *Server) Serve(l net.Listener) error {
	return s.httpSrv.Serve(l)
}

// Shutdown gracefully shuts down the agent and heals active faults.
func (s *Server) Shutdown(ctx context.Context) error {
	s.Teardown()
	_ = s.filter.Close() // remove the agent's nftables tables / iptables rules
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	count := len(s.activeFaults)
	s.mu.Unlock()

	resp := StatusResponse{
		Node:         s.cfg.NodeName,
		Uptime:       int64(time.Since(s.startTime).Seconds()),
		ActiveFaults: count,
		Capabilities: s.Capabilities(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req ApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"bad json: %v"}`, err), http.StatusBadRequest)
		return
	}

	t0 := time.Now().UnixNano()
	resp := s.executeApply(req)
	tEnd := time.Now().UnixNano()

	resp.ReceivedAtNs = t0
	resp.CompletedAtNs = tEnd
	resp.DurationNs = tEnd - t0

	w.Header().Set("Content-Type", "application/json")
	if !resp.Success {
		w.WriteHeader(http.StatusInternalServerError)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) executeApply(req ApplyRequest) ApplyResponse {
	iface := req.Iface
	if iface == "" {
		iface = s.cfg.Interface
	}

	var method string
	var applied string
	var err error

	switch req.Action {
	case ActionInject:
		switch req.Kind {
		case nemesis.Partition:
			method = s.filter.Method()
			err = s.filter.Drop(req.TargetIPs...)
			applied = fmt.Sprintf("partition:dropped:%s", strings.Join(s.filter.Dropped(), ","))

		case nemesis.Latency, nemesis.Loss:
			method = "netlink:netem"
			err = s.netlink.SetNetem(iface, req.Delay, req.Jitter, req.Loss)
			applied = fmt.Sprintf("netem:%s:delay=%v:jitter=%v:loss=%.2f%%", iface, req.Delay, req.Jitter, req.Loss*100)

		case nemesis.Kill:
			method = "process:sigkill"
			pids, e := s.process.Kill(req.Process)
			err = e
			applied = fmt.Sprintf("kill:signaled_pids=%v", pids)
			s.probes.RecordSignal(req.Process, firstPID(pids), "SIGKILL", fmt.Sprintf("agent fault %d kill %s", req.FaultID, req.Process))

		case nemesis.Pause:
			method = "process:sigstop"
			pids, e := s.process.Pause(req.Process)
			err = e
			applied = fmt.Sprintf("pause:signaled_pids=%v", pids)
			s.probes.RecordSignal(req.Process, firstPID(pids), "SIGSTOP", fmt.Sprintf("agent fault %d pause %s", req.FaultID, req.Process))

		case nemesis.Freeze:
			method = "cgroup:freeze"
			err = s.cgroup.Freeze(req.Cgroup)
			applied = fmt.Sprintf("freeze:%s", req.Cgroup)
			s.probes.RecordSignal(req.Cgroup, 0, "CGROUP_FREEZE", fmt.Sprintf("agent fault %d freeze %s", req.FaultID, req.Cgroup))

		case nemesis.IOStall:
			method = "cgroup:io-stall"
			err = s.cgroup.IOStall(req.Cgroup, req.IODevice)
			applied = fmt.Sprintf("io-stall:%s:%s", req.Cgroup, req.IODevice)
			s.probes.Record(probe.Event{
				Kind:    probe.KindFsyncStall,
				Process: req.Cgroup,
				Detail:  fmt.Sprintf("agent fault %d io-stall on %s", req.FaultID, req.IODevice),
			})

		case nemesis.ClockSkew:
			method = "clock:date"
			cmd := exec.Command("sh", "-c", fmt.Sprintf(`date -s "@$(( $(date +%%s) + %d ))" >/dev/null`, int64(req.Offset/time.Second)))
			err = cmd.Run()
			applied = fmt.Sprintf("clock-skew:%v", req.Offset)
			s.probes.Record(probe.Event{
				Kind:       probe.KindClockJump,
				DurationNs: int64(req.Offset),
				Detail:     fmt.Sprintf("agent fault %d clock-skew %v", req.FaultID, req.Offset),
			})

		default:
			if req.Script != "" {
				method = "fallback:shell"
				cmd := exec.Command("sh", "-c", req.Script)
				out, e := cmd.CombinedOutput()
				err = e
				applied = fmt.Sprintf("shell:%s", strings.TrimSpace(string(out)))
			} else {
				err = fmt.Errorf("agent: unsupported fault kind %q", req.Kind)
			}
		}

		if err == nil {
			s.mu.Lock()
			s.activeFaults[req.FaultID] = req
			s.mu.Unlock()
		}

	case ActionHeal:
		switch req.Kind {
		case nemesis.Partition:
			method = s.filter.Method()
			err = s.filter.Flush()
			applied = "partition:healed"

		case nemesis.Latency, nemesis.Loss:
			method = "netlink:netem"
			err = s.netlink.DelNetem(iface)
			applied = fmt.Sprintf("netem:%s:healed", iface)

		case nemesis.Kill:
			method = "process:restart"
			err = s.process.Restart(req.StartCmd)
			applied = fmt.Sprintf("restart:%s", req.StartCmd)

		case nemesis.Pause:
			method = "process:sigcont"
			pids, e := s.process.Resume(req.Process)
			err = e
			applied = fmt.Sprintf("resume:signaled_pids=%v", pids)
			s.probes.RecordSignal(req.Process, firstPID(pids), "SIGCONT", fmt.Sprintf("agent fault %d resume %s", req.FaultID, req.Process))

		case nemesis.Freeze:
			method = "cgroup:freeze"
			err = s.cgroup.Unfreeze(req.Cgroup)
			applied = fmt.Sprintf("unfreeze:%s", req.Cgroup)

		case nemesis.IOStall:
			method = "cgroup:io-stall"
			err = s.cgroup.HealIOStall(req.Cgroup, req.IODevice)
			applied = fmt.Sprintf("heal-io-stall:%s", req.Cgroup)

		case nemesis.ClockSkew:
			method = "clock:date"
			cmd := exec.Command("sh", "-c", fmt.Sprintf(`date -s "@$(( $(date +%%s) - %d ))" >/dev/null`, int64(req.Offset/time.Second)))
			err = cmd.Run()
			applied = fmt.Sprintf("heal-clock-skew:%v", req.Offset)

		default:
			if req.Script != "" {
				method = "fallback:shell"
				cmd := exec.Command("sh", "-c", req.Script)
				out, e := cmd.CombinedOutput()
				err = e
				applied = fmt.Sprintf("shell:%s", strings.TrimSpace(string(out)))
			}
		}

		s.mu.Lock()
		delete(s.activeFaults, req.FaultID)
		s.mu.Unlock()

	case ActionTeardown:
		method = "teardown:all"
		s.Teardown()
		applied = "teardown:complete"
	}

	kernelAt := time.Now().UnixNano()
	resp := ApplyResponse{
		Success:    err == nil,
		Applied:    applied,
		Method:     method,
		KernelAtNs: kernelAt,
	}
	if err != nil {
		resp.Error = err.Error()
	}
	return resp
}

func firstPID(pids []int) int {
	if len(pids) == 0 {
		return 0
	}
	return pids[0]
}

// Teardown idempotently restores all modified network and process state.
func (s *Server) Teardown() {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = s.filter.Flush()
	_ = s.netlink.DelNetem(s.cfg.Interface)
	for _, req := range s.activeFaults {
		if req.Kind == nemesis.Freeze && req.Cgroup != "" {
			_ = s.cgroup.Unfreeze(req.Cgroup)
		}
		if req.Kind == nemesis.IOStall && req.Cgroup != "" {
			_ = s.cgroup.HealIOStall(req.Cgroup, req.IODevice)
		}
		if req.Kind == nemesis.Pause && req.Process != "" {
			_, _ = s.process.Resume(req.Process)
		}
	}
	s.activeFaults = make(map[int]ApplyRequest)
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"bad json: %v"}`, err), http.StatusBadRequest)
		return
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", req.Script)
	out, err := cmd.CombinedOutput()

	resp := ExecResponse{
		Output: strings.TrimSpace(string(out)),
	}
	if err != nil {
		resp.Error = err.Error()
		if exitErr, ok := err.(*exec.ExitError); ok {
			resp.ExitCode = exitErr.ExitCode()
		} else {
			resp.ExitCode = 1
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleProbes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.probes.SampleOnce()
	events := s.probes.Events()
	if r.URL.Query().Get("reset") == "1" {
		s.probes.Reset()
	}
	resp := ProbesResponse{
		Node:   s.cfg.NodeName,
		Events: events,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleTeardown(w http.ResponseWriter, r *http.Request) {
	s.Teardown()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"success":true,"status":"teardown complete"}`))
}
