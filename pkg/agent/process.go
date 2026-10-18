package agent

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// signalTimeout bounds how long a signal fault waits for the kernel to apply
// it: a process in uninterruptible sleep stops or dies only when the call it
// is blocked in returns.
const signalTimeout = 5 * time.Second

// ProcessEngine manages process signals (SIGSTOP, SIGCONT, SIGKILL) with direct syscalls.
type ProcessEngine struct{}

// NewProcessEngine creates a process signal manager.
func NewProcessEngine() *ProcessEngine {
	return &ProcessEngine{}
}

// FindPIDs finds all process IDs matching pattern, excluding strife-agent itself.
func (p *ProcessEngine) FindPIDs(pattern string) ([]int, error) {
	if pattern == "" {
		return nil, fmt.Errorf("process: pattern cannot be empty")
	}

	myPID := os.Getpid()

	// Try reading /proc directly on Linux
	if pids, err := p.scanProc(pattern, myPID); err == nil && len(pids) > 0 {
		return pids, nil
	}

	// Fallback to pgrep command
	cmd := exec.Command("pgrep", "-f", pattern)
	out, err := cmd.Output()
	if err != nil {
		return nil, nil // No matching processes found
	}

	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err == nil && pid != myPID {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func (p *ProcessEngine) scanProc(pattern string, myPID int) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}

	var pids []int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == myPID || pid <= 1 {
			continue
		}

		cmdlinePath := filepath.Join("/proc", entry.Name(), "cmdline")
		data, err := os.ReadFile(cmdlinePath)
		if err != nil {
			continue
		}
		// In /proc/[pid]/cmdline arguments are separated by NUL bytes
		cmdline := string(bytes.ReplaceAll(data, []byte{0}, []byte{' '}))
		if strings.Contains(cmdline, pattern) && !strings.Contains(cmdline, "strife-agent") {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// Pause sends SIGSTOP to all matching processes and returns once each is
// stopped (or gone): a signal is delivered when its target is next
// scheduled, not when kill returns.
func (p *ProcessEngine) Pause(pattern string) ([]int, error) {
	return p.signal(pattern, sendStop, "stopped", func(st byte) bool {
		return st == 'T' || st == 't' || st == 'Z' || st == 'X'
	})
}

// Resume sends SIGCONT to all matching processes and returns once none is
// stopped.
func (p *ProcessEngine) Resume(pattern string) ([]int, error) {
	return p.signal(pattern, sendCont, "running", func(st byte) bool { return st != 'T' })
}

// Kill sends SIGKILL to all matching processes and returns once each has
// exited.
func (p *ProcessEngine) Kill(pattern string) ([]int, error) {
	return p.signal(pattern, sendKill, "dead", func(st byte) bool { return st == 'Z' || st == 'X' })
}

func (p *ProcessEngine) signal(pattern string, send func(int) error, want string, done func(byte) bool) ([]int, error) {
	pids, err := p.FindPIDs(pattern)
	if err != nil {
		return nil, err
	}
	var signaled []int
	for _, pid := range pids {
		if err := send(pid); err == nil {
			signaled = append(signaled, pid)
		}
	}
	return signaled, waitState(signaled, want, done)
}

// waitState waits until every process's state satisfies done or the process
// is gone. Without /proc there is nothing to observe and it returns at once.
func waitState(pids []int, want string, done func(byte) bool) error {
	deadline := time.Now().Add(signalTimeout)
	for _, pid := range pids {
		for {
			st := procState(pid)
			if st == 0 || done(st) {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("process: pid %d still in state %c, not %s, %v after the signal", pid, st, want, signalTimeout)
			}
			time.Sleep(20 * time.Microsecond)
		}
	}
	return nil
}

// procState is the state letter in /proc/<pid>/stat (R, S, D, T, Z…), or 0
// when the process is gone or /proc is unavailable.
func procState(pid int) byte {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	// The command name in parentheses may itself contain ") ".
	i := bytes.LastIndexByte(data, ')')
	if i < 0 || i+2 >= len(data) {
		return 0
	}
	return data[i+2]
}

// Restart executes startCmd in a background shell process.
func (p *ProcessEngine) Restart(startCmd string) error {
	if startCmd == "" {
		return fmt.Errorf("process: restart start_cmd cannot be empty")
	}
	cmd := exec.Command("sh", "-c", startCmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap the shell once the start script returns; the database it
	// launched in the background is re-parented and keeps running.
	go func() { _ = cmd.Wait() }()
	return nil
}
