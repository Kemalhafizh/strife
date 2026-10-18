//go:build linux

package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// nsRecorder is a packet filter that records which network namespace each
// of its calls ran in, and what each sync dropped.
type nsRecorder struct {
	mu         sync.Mutex
	syncNS     []uint64
	syncIPs    [][]net.IP
	teardownNS []uint64
}

func netnsInode(path string) uint64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Sys().(*syscall.Stat_t).Ino
}

func (r *nsRecorder) Name() string    { return "recorder" }
func (r *nsRecorder) Available() bool { return true }
func (r *nsRecorder) Compact() error  { return nil }
func (r *nsRecorder) Teardown() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.teardownNS = append(r.teardownNS, netnsInode("/proc/thread-self/ns/net"))
	return nil
}
func (r *nsRecorder) Sync(ips []net.IP) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.syncNS = append(r.syncNS, netnsInode("/proc/thread-self/ns/net"))
	r.syncIPs = append(r.syncIPs, ips)
	return nil
}

// nsNetlink stands in for netem and records the network namespace of each
// change.
type nsNetlink struct {
	mu       sync.Mutex
	set, del []uint64
}

func (n *nsNetlink) Available() bool { return true }
func (n *nsNetlink) SetNetem(string, time.Duration, time.Duration, float64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.set = append(n.set, netnsInode("/proc/thread-self/ns/net"))
	return nil
}
func (n *nsNetlink) DelNetem(string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.del = append(n.del, netnsInode("/proc/thread-self/ns/net"))
	return nil
}

// A process the agent does not run inside, in a cgroup named after a
// container ID and in network and PID namespaces of its own, is what a pod
// looks like to a DaemonSet agent. Faults that target it must act in its
// network namespace, on its processes only, and on its cgroup.
func TestTargetedContainer(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root: creates a cgroup and network namespaces")
	}
	root := defaultCgroupRoot()
	if root == "" {
		t.Skip("no cgroup v2 hierarchy")
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	id := hex.EncodeToString(raw)
	cg := filepath.Join(root, "strife-test-"+id)
	if err := os.Mkdir(cg, 0o755); err != nil {
		t.Skipf("cannot create a cgroup: %v", err)
	}
	dir, err := os.Open(cg)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()

	marker := strconv.Itoa(1_000_000 + int(raw[0])<<8 | int(raw[1])) // seconds: unique enough, and outlives the test
	inside := exec.Command("sleep", marker)
	inside.Env = []string{} // an empty environment: the agent's must not fill in
	// Like a pod's database: an unprivileged user, with one capability
	// granted (CAP_NET_BIND_SERVICE, ambient).
	inside.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWNET | syscall.CLONE_NEWPID,
		UseCgroupFD: true,
		CgroupFD:    int(dir.Fd()),
		Credential:  &syscall.Credential{Uid: 65534, Gid: 65534},
		AmbientCaps: []uintptr{10},
	}
	if err := inside.Start(); err != nil {
		_ = os.Remove(cg)
		t.Skipf("cannot start a process in a new cgroup and namespaces: %v", err)
	}
	outside := exec.Command("sleep", marker)
	if err := outside.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, c := range []*exec.Cmd{inside, outside} {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
		for i := 0; i < 50 && os.Remove(cg) != nil; i++ {
			time.Sleep(20 * time.Millisecond)
		}
	}()

	t.Setenv("STRIFE_AGENT_TOKEN", "the-agent-secret") // must not reach the container
	rec := &nsRecorder{}
	srv := NewServer(ServerConfig{NodeName: "test", filters: []filterBackend{rec}})
	target := &Target{ContainerID: "containerd://" + id}
	apply := func(req ApplyRequest) ApplyResponse {
		t.Helper()
		req.Target = target
		resp := srv.executeApply(req)
		if !resp.Success {
			t.Fatalf("%s %s: %s", req.Action, req.Kind, resp.Error)
		}
		return resp
	}
	refuse := func(req ApplyRequest, why string) {
		t.Helper()
		req.Target = target
		if resp := srv.executeApply(req); resp.Success || !strings.Contains(resp.Error, why) {
			t.Fatalf("%s %s %q: want refused (%s), got %+v", req.Action, req.Kind, req.Cgroup, why, resp)
		}
	}

	// Partitions are programmed in the container's network namespace.
	apply(ApplyRequest{Action: ActionInject, FaultID: 1, Kind: nemesis.Partition, TargetIPs: []string{"10.0.0.9"}})
	want := netnsInode("/proc/" + strconv.Itoa(inside.Process.Pid) + "/ns/net")
	if len(rec.syncNS) != 1 || rec.syncNS[0] != want || want == netnsInode("/proc/self/ns/net") {
		t.Fatalf("partition synced in netns %v, want the container's %d", rec.syncNS, want)
	}
	apply(ApplyRequest{Action: ActionHeal, FaultID: 1, Kind: nemesis.Partition})
	if len(rec.syncNS) != 2 || rec.syncNS[1] != want {
		t.Fatalf("heal synced in netns %v, want the container's %d", rec.syncNS, want)
	}

	// Signals reach the container's processes and no others.
	apply(ApplyRequest{Action: ActionInject, FaultID: 2, Kind: nemesis.Pause, Process: "sleep " + marker})
	if a, b := procState(inside.Process.Pid), procState(outside.Process.Pid); a != 'T' || b == 'T' {
		t.Fatalf("after pause: container process %c, other process %c; want T and not T", a, b)
	}
	apply(ApplyRequest{Action: ActionHeal, FaultID: 2, Kind: nemesis.Pause, Process: "sleep " + marker})
	if procState(inside.Process.Pid) == 'T' {
		t.Fatal("container process still stopped after resume")
	}

	// "auto" cgroup faults find the container's cgroup.
	resp := apply(ApplyRequest{Action: ActionInject, FaultID: 3, Kind: nemesis.Freeze, Cgroup: "auto"})
	if !strings.HasSuffix(resp.Applied, cg) {
		t.Fatalf("froze %q, want %s", resp.Applied, cg)
	}
	if frozen, _ := readFrozen(filepath.Join(cg, "cgroup.events")); !frozen {
		t.Fatal("cgroup not frozen")
	}
	apply(ApplyRequest{Action: ActionHeal, FaultID: 3, Kind: nemesis.Freeze, Cgroup: "auto"})
	if frozen, _ := readFrozen(filepath.Join(cg, "cgroup.events")); frozen {
		t.Fatal("cgroup still frozen after heal")
	}

	// An explicit cgroup must lie inside the container's: a request for
	// one container cannot freeze the rest of the node.
	for _, other := range []string{root, cg + "/../strife-test-other", "relative/" + id} {
		refuse(ApplyRequest{Action: ActionInject, FaultID: 4, Kind: nemesis.Freeze, Cgroup: other}, "outside container")
	}
	apply(ApplyRequest{Action: ActionInject, FaultID: 4, Kind: nemesis.Freeze, Cgroup: cg + "/"})
	apply(ApplyRequest{Action: ActionHeal, FaultID: 4, Kind: nemesis.Freeze, Cgroup: cg})

	// Scripts for kinds the agent does not implement would run on the
	// node, not in the container.
	refuse(ApplyRequest{Action: ActionInject, FaultID: 5, Kind: "custom", Script: "true"}, "would run on the node")

	// A restart after a kill runs as the container's init does: same user,
	// groups, capabilities, in its cgroup and PID namespace.
	out, err := os.MkdirTemp("", "strife-restart-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(out)
	_ = os.Chmod(out, 0o777)
	initStatus, err := os.ReadFile("/proc/" + strconv.Itoa(inside.Process.Pid) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	// Both ways into the cgroup: cloned into it, and (kernels before 5.7)
	// moved there before nsenter starts.
	for i, clone := range []bool{true, false} {
		cloneIntoCgroup = clone
		report := filepath.Join(out, "status"+strconv.Itoa(i))
		apply(ApplyRequest{Action: ActionHeal, FaultID: 6, Kind: nemesis.Kill,
			StartCmd: "grep -E '^(Uid|Gid|Groups|Cap[A-Za-z]+|NoNewPrivs):' /proc/self/status > " + report + ".tmp; " +
				"cat /proc/self/cgroup >> " + report + ".tmp; echo $$ ${STRIFE_AGENT_TOKEN:-no-token} >> " + report + ".tmp; mv " + report + ".tmp " + report})
		var got []byte
		for i := 0; i < 250 && got == nil; i++ {
			got, _ = os.ReadFile(report)
			time.Sleep(20 * time.Millisecond)
		}
		if got == nil {
			t.Fatalf("clone=%v: the restarted command never ran", clone)
		}
		for _, line := range strings.Split(string(initStatus), "\n") {
			key, _, _ := strings.Cut(line, ":")
			switch key {
			case "Uid", "Gid", "Groups", "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs":
				if !strings.Contains(string(got), line+"\n") {
					t.Errorf("clone=%v: restarted with %s different from the container's init (%q):\n%s", clone, key, line, got)
				}
			}
		}
		if !strings.Contains(string(got), "strife-test-"+id) {
			t.Errorf("clone=%v: restart ran outside the container's cgroup:\n%s", clone, got)
		}
		if fields := strings.Fields(string(got)); len(fields) < 2 || fields[len(fields)-2] == strconv.Itoa(inside.Process.Pid) || fields[len(fields)-1] != "no-token" {
			t.Errorf("clone=%v: restart outside the container's PID namespace, or with the agent's environment:\n%s", clone, got)
		}
	}
	cloneIntoCgroup = true

	// A container sharing the node's network (hostNetwork) gets no network
	// faults: they would land on the node.
	hostNet, _ := startInCgroup(t, root, "strife-test-"+strings.Repeat("c", 64), syscall.CLONE_NEWPID, "sleep", marker)
	for _, kind := range []nemesis.Kind{nemesis.Partition, nemesis.Latency, nemesis.Loss} {
		req := ApplyRequest{Action: ActionInject, FaultID: 7, Kind: kind, TargetIPs: []string{"10.0.0.9"}, Delay: time.Millisecond, Target: &Target{ContainerID: hostNet}}
		if resp := srv.executeApply(req); resp.Success || !strings.Contains(resp.Error, "uses its node's network") {
			t.Errorf("%s on a hostNetwork container: %+v", kind, resp)
		}
	}

	// A container that does not run here is reported as unknown.
	_, err = srv.scopeFor(ApplyRequest{Target: &Target{ContainerID: strings.Repeat("ab", 32)}})
	if !errors.Is(err, ErrUnknownContainer) {
		t.Fatalf("unknown container: %v", err)
	}
}

// startInCgroup starts argv in a new cgroup named name under root, in new
// namespaces per cloneflags, and returns the container ID the name ends in.
// Both are removed when the test ends.
func startInCgroup(t *testing.T, root, name string, cloneflags uintptr, argv ...string) (string, *exec.Cmd) {
	t.Helper()
	cg := filepath.Join(root, name)
	if err := os.Mkdir(cg, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(cg)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: cloneflags, UseCgroupFD: true, CgroupFD: int(dir.Fd())}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(cg)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		for i := 0; i < 50 && os.Remove(cg) != nil; i++ {
			time.Sleep(20 * time.Millisecond)
		}
	})
	return name[strings.LastIndexByte(name, '-')+1:], cmd
}

// A pod's network namespace belongs to its sandbox, not to its containers:
// when a container restarts, it comes back with a new ID and init in the
// same namespace. Faults applied through the old container must be healed
// there, through the new one or by a teardown after the old one is gone.
func TestPodNetnsOutlivesContainer(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root: creates cgroups and namespaces")
	}
	root := defaultCgroupRoot()
	if root == "" {
		t.Skip("no cgroup v2 hierarchy")
	}
	if _, err := exec.LookPath("nsenter"); err != nil {
		t.Skip("needs nsenter")
	}
	raw := make([]byte, 64)
	_, _ = rand.Read(raw)
	marker := strconv.Itoa(1_000_000 + int(raw[0])<<8 | int(raw[1]))
	pause := exec.Command("sleep", marker) // the sandbox: holds the pod's network namespace
	pause.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
	if err := pause.Start(); err != nil {
		t.Skipf("cannot create a network namespace: %v", err)
	}
	defer func() { _ = pause.Process.Kill(); _ = pause.Wait() }()
	podNet := "/proc/" + strconv.Itoa(pause.Process.Pid) + "/ns/net"
	want := netnsInode(podNet)
	inPod := func(id string) (string, *exec.Cmd) {
		id, cmd := startInCgroup(t, root, "strife-test-"+id, syscall.CLONE_NEWPID, "nsenter", "--net="+podNet, "--", "sleep", marker)
		for i := 0; netnsInode("/proc/"+strconv.Itoa(cmd.Process.Pid)+"/ns/net") != want; i++ {
			if i == 200 {
				t.Fatal("the container never joined the pod's network namespace")
			}
			time.Sleep(10 * time.Millisecond) // nsenter has not switched yet
		}
		return id, cmd
	}

	rec, nl := &nsRecorder{}, &nsNetlink{}
	srv := NewServer(ServerConfig{NodeName: "test", RequireTarget: true, filters: []filterBackend{rec}})
	srv.netlink = nl
	apply := func(id string, req ApplyRequest) {
		t.Helper()
		req.Target = &Target{ContainerID: id}
		if resp := srv.executeApply(req); !resp.Success {
			t.Fatalf("%s %s on %.12s: %s", req.Action, req.Kind, id, resp.Error)
		}
	}
	idA, a := inPod(hex.EncodeToString(raw[:32]))
	apply(idA, ApplyRequest{Action: ActionInject, FaultID: 1, Kind: nemesis.Partition, TargetIPs: []string{"10.0.0.9"}})
	apply(idA, ApplyRequest{Action: ActionInject, FaultID: 2, Kind: nemesis.Latency, Delay: time.Millisecond})
	apply(idA, ApplyRequest{Action: ActionInject, FaultID: 3, Kind: nemesis.Loss, Loss: 0.1})
	if len(rec.syncNS) != 1 || rec.syncNS[0] != want || len(nl.set) != 2 || nl.set[0] != want {
		t.Fatalf("faults not applied in the pod's namespace %d: filter %v, netem %v", want, rec.syncNS, nl.set)
	}

	// The container restarts: its init is gone. Before the new one runs,
	// the transport has no other ID to send: the heal through the old one
	// reaches the pod's namespace all the same.
	_ = a.Process.Kill()
	_ = a.Wait()
	apply(idA, ApplyRequest{Action: ActionHeal, FaultID: 2, Kind: nemesis.Latency})
	if len(nl.del) != 1 || nl.del[0] != want {
		t.Fatalf("netem not removed from the pod's namespace %d through the gone container: %v", want, nl.del)
	}
	if resp := srv.executeApply(ApplyRequest{Action: ActionHeal, FaultID: 9, Kind: nemesis.Latency, Target: &Target{ContainerID: idA}}); resp.Success {
		t.Fatalf("healed a fault never applied through a gone container: %+v", resp)
	}

	// Once the new container runs, the transport sends the heal through it.
	idB, _ := inPod(hex.EncodeToString(raw[32:]))
	apply(idB, ApplyRequest{Action: ActionHeal, FaultID: 1, Kind: nemesis.Partition})
	if last := len(rec.syncNS) - 1; last < 1 || rec.syncNS[last] != want || len(rec.syncIPs[last]) != 0 {
		t.Fatalf("partition not healed in the pod's namespace %d: synced %v with %v", want, rec.syncNS, rec.syncIPs)
	}
	// Both heals cleared the faults' records, the one through the
	// replacement included; only the loss fault is left on.
	srv.mu.Lock()
	left := slices.Collect(maps.Keys(srv.activeFaults))
	srv.mu.Unlock()
	if len(left) != 1 || left[0] != "3/"+idA {
		t.Fatalf("faults on record after the heals: %v", left)
	}

	// The loss fault, applied through the old container, is still on:
	// teardown heals it in the pod's namespace and removes the filter
	// there.
	if err := srv.Teardown(); err != nil {
		t.Fatal(err)
	}
	if len(nl.del) != 2 || nl.del[1] != want {
		t.Fatalf("netem not removed from the pod's namespace %d: %v", want, nl.del)
	}
	if len(rec.teardownNS) != 1 || rec.teardownNS[0] != want {
		t.Fatalf("filter not torn down in the pod's namespace %d: %v", want, rec.teardownNS)
	}
}

// The helper narrows a root process to the container's bounding set and
// capabilities, and sets no_new_privs when the container's init has it.
func TestStartAsNarrowsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root: sets credentials")
	}
	out := filepath.Join(t.TempDir(), "status")
	spec := `{"uid":0,"gid":0,"cap_bnd":3,"cap_prm":3,"cap_eff":3,"cap_inh":0,"cap_amb":0,"no_new_privs":true}`
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, helperArg, spec, "grep -E '^(Cap[A-Za-z]+|NoNewPrivs):' /proc/self/status > "+out)
	cmd.ExtraFiles = []*os.File{devnull, w}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if err := awaitHelper(r, done); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CapBnd:\t0000000000000003", "CapEff:\t0000000000000003", "CapPrm:\t0000000000000003", "CapInh:\t0000000000000000", "NoNewPrivs:\t1"} {
		if !strings.Contains(string(got), want+"\n") {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
}
