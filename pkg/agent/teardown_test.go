package agent

import (
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// A database killed and never healed (the run was interrupted, or its
// restart failed) is restarted by teardown, but never next to a copy that
// already runs.
func TestTeardownRestartsKilledDatabase(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("finds processes through /proc")
	}
	marker := strconv.Itoa(2_000_000 + rand.IntN(1_000_000))
	pattern := "sleep " + marker
	starts := filepath.Join(t.TempDir(), "starts")
	// The start command's own command line does not match the pattern.
	start := "echo start >> " + starts + "; exec sleep $((" + marker + "))"
	srv := NewServer(ServerConfig{NodeName: "n", Interface: "lo", filters: []filterBackend{&fakeFilter{}}})
	srv.netlink = &recordNetlink{}
	t.Cleanup(func() { _, _ = srv.process.Kill(pattern) })

	run := func() *exec.Cmd {
		t.Helper()
		db := exec.Command("sleep", marker)
		if err := db.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { _ = db.Wait() }()
		return db
	}
	kill := func(id int) {
		t.Helper()
		resp := srv.executeApply(ApplyRequest{Action: ActionInject, FaultID: id, Kind: nemesis.Kill, Process: pattern, StartCmd: start})
		if !resp.Success || resp.Applied == "kill:signaled_pids=[]" {
			t.Fatalf("kill %d: %+v", id, resp)
		}
	}
	// startsBy counts the starts once want have been written (the start
	// shell runs on after the restart returns), or after half a second.
	startsBy := func(want int) int {
		for i := 0; ; i++ {
			data, _ := os.ReadFile(starts)
			if n := strings.Count(string(data), "\n"); n >= want || i == 50 {
				return n
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	running := func() bool {
		for i := 0; i < 200; i++ {
			if pids, _ := srv.process.FindPIDs(pattern); len(pids) > 0 {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}

	run()
	kill(1)
	if err := srv.Teardown(); err != nil {
		t.Fatal(err)
	}
	if n := startsBy(1); n != 1 || !running() {
		t.Fatalf("teardown after an unhealed kill: %d starts, running %v", n, running())
	}

	// Killed again, and back before teardown (the kubelet restarted its
	// container): teardown leaves it be.
	kill(2)
	run()
	if err := srv.Teardown(); err != nil {
		t.Fatal(err)
	}
	if n := startsBy(2); n != 1 {
		t.Fatalf("teardown started a second copy: %d starts", n)
	}

	// A heal that fails keeps the kill on record for teardown to retry.
	kill(3)
	if resp := srv.executeApply(ApplyRequest{Action: ActionHeal, FaultID: 3, Kind: nemesis.Kill, Process: pattern}); resp.Success {
		t.Fatalf("a restart without a start command succeeded: %+v", resp)
	}
	if err := srv.Teardown(); err != nil {
		t.Fatal(err)
	}
	if n := startsBy(2); n != 2 || !running() {
		t.Fatalf("teardown after a failed heal: %d starts, running %v", n, running())
	}

	// A heal finds the database running already: it says so and starts
	// nothing.
	kill(4)
	run()
	resp := srv.executeApply(ApplyRequest{Action: ActionHeal, FaultID: 4, Kind: nemesis.Kill, Process: pattern, StartCmd: start})
	if !resp.Success || resp.Applied != "restart:already-running" {
		t.Fatalf("heal next to a running copy: %+v", resp)
	}
	if n := startsBy(3); n != 2 {
		t.Fatalf("the heal started a second copy: %d starts", n)
	}
}

// Teardown removes netem from the interface a request named, not only
// from the agent's -iface.
func TestTeardownHealsNamedInterface(t *testing.T) {
	srv := NewServer(ServerConfig{NodeName: "n", Interface: "lo", filters: []filterBackend{&fakeFilter{}}})
	nl := &recordNetlink{}
	srv.netlink = nl
	if resp := srv.executeApply(ApplyRequest{Action: ActionInject, FaultID: 1, Kind: nemesis.Latency, Iface: "eth9", Delay: time.Millisecond}); !resp.Success {
		t.Fatal(resp.Error)
	}
	if err := srv.Teardown(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(nl.deleted, ",") != "lo,eth9" {
		t.Fatalf("teardown removed netem from %v, want lo and eth9", nl.deleted)
	}
}
