//go:build linux

package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// nsRecorder is a packet filter that records which network namespace each
// of its calls ran in.
type nsRecorder struct {
	mu     sync.Mutex
	syncNS []uint64
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
func (r *nsRecorder) Teardown() error { return nil }
func (r *nsRecorder) Sync([]net.IP) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.syncNS = append(r.syncNS, netnsInode("/proc/thread-self/ns/net"))
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
	inside.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWNET | syscall.CLONE_NEWPID,
		UseCgroupFD: true,
		CgroupFD:    int(dir.Fd()),
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

	// A container that does not run here is reported as unknown.
	_, err = srv.scopeFor(ApplyRequest{Target: &Target{ContainerID: strings.Repeat("ab", 32)}})
	if !errors.Is(err, ErrUnknownContainer) {
		t.Fatalf("unknown container: %v", err)
	}
}
