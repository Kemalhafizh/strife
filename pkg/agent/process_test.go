package agent

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// A signal fault returns only once the kernel has applied it: the process
// is stopped when Pause returns, running when Resume returns and dead when
// Kill returns, every time.
func TestSignalsTakeEffectBeforeReturning(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process states are read from /proc")
	}
	marker := fmt.Sprintf("%d.%d", 40+os.Getpid()%1000, time.Now().Nanosecond()%1000)
	var procs []*exec.Cmd
	for i := 0; i < 3; i++ {
		c := exec.Command("sleep", marker)
		if err := c.Start(); err != nil {
			t.Skipf("cannot start a process to signal: %v", err)
		}
		procs = append(procs, c)
	}
	defer func() {
		for _, c := range procs {
			_ = c.Process.Kill()
			_ = c.Wait()
		}
	}()
	pattern := "sleep " + marker
	e := NewProcessEngine()
	states := func() string {
		s := ""
		for _, c := range procs {
			s += string(procState(c.Process.Pid))
		}
		return s
	}

	for round := 0; round < 50; round++ {
		pids, err := e.Pause(pattern)
		if err != nil || len(pids) != len(procs) {
			t.Fatalf("round %d: pause: %v %v", round, pids, err)
		}
		if st := states(); st != "TTT" {
			t.Fatalf("round %d: states after pause %q, want TTT", round, st)
		}
		if _, err := e.Resume(pattern); err != nil {
			t.Fatalf("round %d: resume: %v", round, err)
		}
		for _, r := range states() {
			if r == 'T' {
				t.Fatalf("round %d: a process is still stopped after resume (%q)", round, states())
			}
		}
	}

	if _, err := e.Kill(pattern); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if st := states(); st != "ZZZ" {
		t.Fatalf("states after kill %q, want ZZZ (dead, not yet reaped)", st)
	}
}

func TestProcStateOfAGoneProcess(t *testing.T) {
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Skip(err)
	}
	if st := procState(c.Process.Pid); st != 0 {
		t.Fatalf("procState of a reaped process = %c, want 0", st)
	}
	if err := waitState([]int{c.Process.Pid}, "stopped", func(byte) bool { return false }); err != nil {
		t.Fatalf("waiting on a gone process: %v", err)
	}
}
