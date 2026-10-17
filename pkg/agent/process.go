package agent

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

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

// Pause sends SIGSTOP to all matching processes.
func (p *ProcessEngine) Pause(pattern string) ([]int, error) {
	pids, err := p.FindPIDs(pattern)
	if err != nil {
		return nil, err
	}
	var signaled []int
	for _, pid := range pids {
		if err := sendStop(pid); err == nil {
			signaled = append(signaled, pid)
		}
	}
	return signaled, nil
}

// Resume sends SIGCONT to all matching processes.
func (p *ProcessEngine) Resume(pattern string) ([]int, error) {
	pids, err := p.FindPIDs(pattern)
	if err != nil {
		return nil, err
	}
	var signaled []int
	for _, pid := range pids {
		if err := sendCont(pid); err == nil {
			signaled = append(signaled, pid)
		}
	}
	return signaled, nil
}

// Kill sends SIGKILL to all matching processes.
func (p *ProcessEngine) Kill(pattern string) ([]int, error) {
	pids, err := p.FindPIDs(pattern)
	if err != nil {
		return nil, err
	}
	var signaled []int
	for _, pid := range pids {
		if err := sendKill(pid); err == nil {
			signaled = append(signaled, pid)
		}
	}
	return signaled, nil
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
