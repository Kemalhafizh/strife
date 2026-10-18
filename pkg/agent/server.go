package agent

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

	// CgroupRoot is where the agent sees the node's cgroup v2 filesystem,
	// for requests that target another container (default /sys/fs/cgroup,
	// or its unified half on a hybrid host). A DaemonSet mounts the node's
	// at e.g. /host/sys/fs/cgroup.
	CgroupRoot string
	// IORoot is a directory on the disk that holds container data; an
	// io-stall with "io_device": "auto" stalls that disk.
	IORoot string
	// RequireTarget makes the agent act only on target containers, never
	// on the node: an agent that shares its node's network and PID
	// namespaces (a DaemonSet) would otherwise partition, slow down or
	// signal the node itself when a request names no container. It also
	// turns off /exec and the shell fallback, and teardown leaves the
	// node's interface and packet filter alone.
	RequireTarget bool

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

	containers *containerIndex
	ownNetns   string // the agent's network namespace, read at start
	nsMu       sync.Mutex
	nsFilters  map[string]*nsFilter // by network namespace of the targets

	mu           sync.Mutex
	activeFaults map[string]activeFault // by faultKey
}

// nsFilter is the packet filter inside a network namespace that target
// containers live in, shared by the containers of one pod. It holds the
// namespace open: a container that restarts inside its pod gets a new ID
// and init, but its rules stay in the pod's namespace and must be healed
// there.
type nsFilter struct {
	ns     *os.File
	engine *FilterEngine
}

// activeFault is a fault in effect, resolved: its interface, cgroup and
// device, and the network namespace it was applied in ("" for the agent's
// own).
type activeFault struct {
	req   ApplyRequest
	netns string
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
		containers: newContainerIndex("/proc", cfg.CgroupRoot),
		ownNetns:   nsOf("/proc", "thread-self", "net"),
		nsFilters:  map[string]*nsFilter{},

		activeFaults: make(map[string]activeFault),
	}
	if cfg.CgroupRoot == "" {
		s.containers.cgroupRoot = defaultCgroupRoot()
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
		NFTables:      !s.cfg.RequireTarget && s.filter.Method() == "filter:nftables",
		IPTables:      !s.cfg.RequireTarget && s.filter.Method() == "filter:iptables",
		TC:            s.netlink.Available(),
		Containers:    runtime.GOOS == "linux",
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

// scope is where a request acts: the agent's own namespaces, or those of
// the container it targets.
type scope struct {
	c      *container // nil: the agent's own
	filter *FilterEngine
	ns     *os.File // the container's network namespace
	netns  string   // its name, "net:[inode]"
}

// run runs fn in the scope's network namespace.
func (sc scope) run(fn func() error) error {
	if sc.ns == nil {
		return fn()
	}
	return inNetns(sc.ns, fn)
}

// owns reports whether pid belongs to the scope (nil: every process).
func (sc scope) owns() func(pid int) bool {
	if sc.c == nil {
		return nil
	}
	return ownedBy(sc.c)
}

func ownedBy(c *container) func(pid int) bool {
	return func(pid int) bool { return containerHas(pid, c.id) }
}

func containerHas(pid int, id string) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	return err == nil && strings.Contains(string(data), id)
}

// scopeFor resolves where req acts.
func (s *Server) scopeFor(req ApplyRequest) (scope, error) {
	if req.Target == nil {
		return scope{filter: s.filter}, nil
	}
	c, err := s.containers.resolve(req.Target.ContainerID)
	if err != nil {
		return scope{}, err
	}
	ns, key, err := openNetns(c.pid)
	if err != nil {
		return scope{}, err
	}
	s.nsMu.Lock()
	defer s.nsMu.Unlock()
	f, ok := s.nsFilters[key]
	if ok {
		ns.Close()
	} else {
		e := NewFilterEngine()
		if s.cfg.filters != nil {
			e.backends = s.cfg.filters
		}
		e.run = func(fn func() error) error { return inNetns(ns, fn) }
		f = &nsFilter{ns: ns, engine: e}
		s.nsFilters[key] = f
	}
	return scope{c: c, filter: f.engine, ns: f.ns, netns: key}, nil
}

// onNodeNetwork reports whether netns is the node's network namespace (that
// of the node's init, which a DaemonSet agent sees) or the agent's own: a
// network fault "in a container" there would hit the node.
func (s *Server) onNodeNetwork(netns string) bool {
	return netns != "" && (netns == s.ownNetns || netns == nsOf("/proc", "1", "net"))
}

// faultKey identifies an active fault: one fault spans several containers
// when an agent serves a whole node.
func faultKey(req ApplyRequest) string {
	if req.Target == nil {
		return strconv.Itoa(req.FaultID)
	}
	return strconv.Itoa(req.FaultID) + "/" + req.Target.ContainerID
}

// cgroupOf is the cgroup a request's cgroup fault acts on: the one it
// names, or the target container's for "auto". A request that targets a
// container may only name a cgroup inside that container's, so it cannot
// freeze or throttle the rest of the node.
func (sc scope) cgroupOf(req ApplyRequest) (string, error) {
	if sc.c == nil {
		return req.Cgroup, nil
	}
	if sc.c.cgroup == "" {
		return "", fmt.Errorf("agent: no cgroup v2 directory found for container %.12s (set -cgroup-root to the node's cgroup filesystem)", sc.c.id)
	}
	if req.Cgroup == "" || req.Cgroup == "auto" {
		return sc.c.cgroup, nil
	}
	if p := filepath.Clean(req.Cgroup); p == sc.c.cgroup || strings.HasPrefix(p, sc.c.cgroup+"/") {
		return p, nil
	}
	return "", fmt.Errorf("agent: cgroup %s is outside container %.12s's cgroup %s (use \"auto\")", req.Cgroup, sc.c.id, sc.c.cgroup)
}

// ioDevice resolves "auto" to the disk under the configured IORoot.
func (s *Server) ioDevice(dev string) (string, error) {
	if dev != "auto" {
		return dev, nil
	}
	if s.cfg.IORoot == "" {
		return "", fmt.Errorf("agent: io_device \"auto\" needs -io-root, a directory on the disk that holds container data")
	}
	return diskOf("/sys", s.cfg.IORoot)
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
		if req.Target != nil {
			iface = "eth0" // the container's, not the node's
		}
	}

	var method string
	var applied string
	var err error

	if s.cfg.RequireTarget && req.Target == nil && req.Action != ActionTeardown {
		return ApplyResponse{Success: false, Error: "agent: this agent serves a whole node and acts only on a target container; the request names none", KernelAtNs: time.Now().UnixNano()}
	}
	sc, err := s.scopeFor(req)
	if err != nil {
		return ApplyResponse{Success: false, Error: err.Error(), KernelAtNs: time.Now().UnixNano()}
	}
	cgroup, ioDev := req.Cgroup, req.IODevice
	if req.Kind == nemesis.Freeze || req.Kind == nemesis.IOStall {
		if cgroup, err = sc.cgroupOf(req); err == nil && req.Kind == nemesis.IOStall {
			ioDev, err = s.ioDevice(req.IODevice)
		}
		if err != nil {
			return ApplyResponse{Success: false, Error: err.Error(), KernelAtNs: time.Now().UnixNano()}
		}
	}
	if sc.c != nil && req.Kind == nemesis.ClockSkew {
		return ApplyResponse{Success: false, Error: "agent: containers share their node's clock; clock-skew would skew the whole node", KernelAtNs: time.Now().UnixNano()}
	}
	if sc.c != nil && (req.Kind == nemesis.Partition || req.Kind == nemesis.Latency || req.Kind == nemesis.Loss) && s.onNodeNetwork(sc.netns) {
		return ApplyResponse{Success: false, Error: fmt.Sprintf("agent: container %.12s uses its node's network (hostNetwork); a %s fault there would hit the whole node", sc.c.id, req.Kind), KernelAtNs: time.Now().UnixNano()}
	}

	switch req.Action {
	case ActionInject:
		switch req.Kind {
		case nemesis.Partition:
			method = sc.filter.Method()
			err = sc.filter.Drop(req.TargetIPs...)
			applied = fmt.Sprintf("partition:dropped:%s", strings.Join(sc.filter.Dropped(), ","))

		case nemesis.Latency, nemesis.Loss:
			method = "netlink:netem"
			err = sc.run(func() error { return s.netlink.SetNetem(iface, req.Delay, req.Jitter, req.Loss) })
			applied = fmt.Sprintf("netem:%s:delay=%v:jitter=%v:loss=%.2f%%", iface, req.Delay, req.Jitter, req.Loss*100)

		case nemesis.Kill:
			method = "process:sigkill"
			pids, e := s.process.KillIn(req.Process, sc.owns())
			err = e
			applied = fmt.Sprintf("kill:signaled_pids=%v", pids)
			s.probes.RecordSignal(req.Process, firstPID(pids), "SIGKILL", fmt.Sprintf("agent fault %d kill %s", req.FaultID, req.Process))

		case nemesis.Pause:
			method = "process:sigstop"
			pids, e := s.process.PauseIn(req.Process, sc.owns())
			err = e
			applied = fmt.Sprintf("pause:signaled_pids=%v", pids)
			s.probes.RecordSignal(req.Process, firstPID(pids), "SIGSTOP", fmt.Sprintf("agent fault %d pause %s", req.FaultID, req.Process))

		case nemesis.Freeze:
			method = "cgroup:freeze"
			err = s.cgroup.Freeze(cgroup)
			applied = fmt.Sprintf("freeze:%s", cgroup)
			s.probes.RecordSignal(cgroup, 0, "CGROUP_FREEZE", fmt.Sprintf("agent fault %d freeze %s", req.FaultID, cgroup))

		case nemesis.IOStall:
			method = "cgroup:io-stall"
			err = s.cgroup.IOStall(cgroup, ioDev)
			applied = fmt.Sprintf("io-stall:%s:%s", cgroup, ioDev)
			s.probes.Record(probe.Event{
				Kind:    probe.KindFsyncStall,
				Process: cgroup,
				Detail:  fmt.Sprintf("agent fault %d io-stall on %s", req.FaultID, ioDev),
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
			switch {
			case req.Script == "":
				err = fmt.Errorf("agent: unsupported fault kind %q", req.Kind)
			case sc.c != nil || s.cfg.RequireTarget:
				err = s.noNodeScript(req.Kind)
			default:
				method = "fallback:shell"
				cmd := exec.Command("sh", "-c", req.Script)
				out, e := cmd.CombinedOutput()
				err = e
				applied = fmt.Sprintf("shell:%s", strings.TrimSpace(string(out)))
			}
		}

		if err == nil {
			stored := req
			stored.Iface, stored.Cgroup, stored.IODevice = iface, cgroup, ioDev
			s.mu.Lock()
			s.activeFaults[faultKey(req)] = activeFault{req: stored, netns: sc.netns}
			s.mu.Unlock()
		}

	case ActionHeal:
		switch req.Kind {
		case nemesis.Partition:
			method = sc.filter.Method()
			err = sc.filter.Flush()
			applied = "partition:healed"

		case nemesis.Latency, nemesis.Loss:
			method = "netlink:netem"
			err = sc.run(func() error { return s.netlink.DelNetem(iface) })
			applied = fmt.Sprintf("netem:%s:healed", iface)

		case nemesis.Kill:
			method = "process:restart"
			if sc.c != nil {
				err = s.process.RestartIn(req.StartCmd, sc.c)
			} else {
				err = s.process.Restart(req.StartCmd)
			}
			applied = fmt.Sprintf("restart:%s", req.StartCmd)

		case nemesis.Pause:
			method = "process:sigcont"
			pids, e := s.process.ResumeIn(req.Process, sc.owns())
			err = e
			applied = fmt.Sprintf("resume:signaled_pids=%v", pids)
			s.probes.RecordSignal(req.Process, firstPID(pids), "SIGCONT", fmt.Sprintf("agent fault %d resume %s", req.FaultID, req.Process))

		case nemesis.Freeze:
			method = "cgroup:freeze"
			err = s.cgroup.Unfreeze(cgroup)
			applied = fmt.Sprintf("unfreeze:%s", cgroup)

		case nemesis.IOStall:
			method = "cgroup:io-stall"
			err = s.cgroup.HealIOStall(cgroup, ioDev)
			applied = fmt.Sprintf("heal-io-stall:%s", cgroup)

		case nemesis.ClockSkew:
			method = "clock:date"
			cmd := exec.Command("sh", "-c", fmt.Sprintf(`date -s "@$(( $(date +%%s) - %d ))" >/dev/null`, int64(req.Offset/time.Second)))
			err = cmd.Run()
			applied = fmt.Sprintf("heal-clock-skew:%v", req.Offset)

		default:
			switch {
			case req.Script == "":
			case sc.c != nil || s.cfg.RequireTarget:
				err = s.noNodeScript(req.Kind)
			default:
				method = "fallback:shell"
				cmd := exec.Command("sh", "-c", req.Script)
				out, e := cmd.CombinedOutput()
				err = e
				applied = fmt.Sprintf("shell:%s", strings.TrimSpace(string(out)))
			}
		}

		s.mu.Lock()
		delete(s.activeFaults, faultKey(req))
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

// noNodeScript refuses the shell fallback for a fault kind the agent does
// not implement: the script would run in the agent's namespaces, on the
// node, not in the target container.
func (s *Server) noNodeScript(kind nemesis.Kind) error {
	return fmt.Errorf("agent: fault kind %q has no built-in implementation, and its script would run on the node, not in the target container", kind)
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

	if !s.cfg.RequireTarget {
		// An agent that serves the node itself; with RequireTarget the
		// agent never touched the node's own filter or interface.
		_ = s.filter.Flush()
		_ = s.netlink.DelNetem(s.cfg.Interface)
	}
	// Faults stored resolved: cgroup paths and the device, the interface,
	// the network namespace. Network faults are healed in the namespace
	// they were applied in, which outlives a container restarting in its
	// pod; then the agent removes its tables there and lets go of it.
	s.nsMu.Lock()
	for _, a := range s.activeFaults {
		if a.req.Kind == nemesis.Latency || a.req.Kind == nemesis.Loss {
			if f := s.nsFilters[a.netns]; f != nil {
				_ = inNetns(f.ns, func() error { return s.netlink.DelNetem(a.req.Iface) })
			}
		}
	}
	for key, f := range s.nsFilters {
		_ = f.engine.Close()
		_ = f.ns.Close()
		delete(s.nsFilters, key)
	}
	s.nsMu.Unlock()
	for _, a := range s.activeFaults {
		req := a.req
		if req.Kind == nemesis.Freeze && req.Cgroup != "" {
			_ = s.cgroup.Unfreeze(req.Cgroup)
		}
		if req.Kind == nemesis.IOStall && req.Cgroup != "" {
			_ = s.cgroup.HealIOStall(req.Cgroup, req.IODevice)
		}
		if req.Kind == nemesis.Pause && req.Process != "" {
			// A container that is gone has no stopped process left.
			var owns func(int) bool
			if req.Target != nil {
				c, err := s.containers.resolve(req.Target.ContainerID)
				if err != nil {
					continue
				}
				owns = ownedBy(c)
			}
			_, _ = s.process.ResumeIn(req.Process, owns)
		}
	}
	s.activeFaults = make(map[string]activeFault)
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cfg.RequireTarget {
		// Scripts would run on the node, with the agent's privileges.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(ExecResponse{Error: "agent: this agent acts only on target containers and runs no scripts on the node (-require-target)", ExitCode: 1})
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
