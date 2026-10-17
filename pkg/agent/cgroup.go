package agent

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// freezeTimeout bounds how long Freeze waits for the kernel to report the
// cgroup frozen.
const freezeTimeout = 5 * time.Second

// CgroupEngine provides zero-overhead direct file writes to cgroup v2 controllers.
type CgroupEngine struct{}

// NewCgroupEngine creates a cgroup v2 manager.
func NewCgroupEngine() *CgroupEngine {
	return &CgroupEngine{}
}

// Available reports whether a cgroup v2 hierarchy is mounted.
func (c *CgroupEngine) Available() bool {
	_, err := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	return err == nil
}

// Freeze sets cgroup.freeze to 1, suspending all processes in the cgroup.
func (c *CgroupEngine) Freeze(cgroupPath string) error {
	if cgroupPath == "" {
		return fmt.Errorf("cgroup: path cannot be empty")
	}
	target := filepath.Join(cgroupPath, "cgroup.freeze")
	if err := os.WriteFile(target, []byte("1\n"), 0o644); err != nil {
		return err
	}
	return waitFrozen(cgroupPath, true, freezeTimeout)
}

// waitFrozen polls cgroup.events until the kernel reports the wanted state.
// Freezing is asynchronous: writing cgroup.freeze only requests it, and a
// task stuck in an uninterruptible syscall delays it. A fault that is
// reported as applied must really be in effect.
func waitFrozen(cgroupPath string, want bool, timeout time.Duration) error {
	events := filepath.Join(cgroupPath, "cgroup.events")
	if _, err := os.Stat(events); err != nil {
		return nil // not a real cgroup (tests) or an old kernel
	}
	deadline := time.Now().Add(timeout)
	for {
		frozen, err := readFrozen(events)
		if err != nil {
			return err
		}
		if frozen == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cgroup %s: still frozen=%v after %v", cgroupPath, frozen, timeout)
		}
		time.Sleep(time.Millisecond)
	}
}

func readFrozen(events string) (bool, error) {
	f, err := os.Open(events)
	if err != nil {
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "frozen "); ok {
			return v == "1", nil
		}
	}
	return false, sc.Err()
}

// Unfreeze sets cgroup.freeze to 0, resuming all processes in the cgroup.
func (c *CgroupEngine) Unfreeze(cgroupPath string) error {
	if cgroupPath == "" {
		return fmt.Errorf("cgroup: path cannot be empty")
	}
	target := filepath.Join(cgroupPath, "cgroup.freeze")
	if err := os.WriteFile(target, []byte("0\n"), 0o644); err != nil {
		return err
	}
	return waitFrozen(cgroupPath, false, freezeTimeout)
}

// IOStall throttles disk read/write bandwidth and IOPS to near-zero via io.max.
func (c *CgroupEngine) IOStall(cgroupPath, ioDevice string) error {
	if cgroupPath == "" || ioDevice == "" {
		return fmt.Errorf("cgroup: path and io_device cannot be empty")
	}
	target := filepath.Join(cgroupPath, "io.max")
	content := fmt.Sprintf("%s rbps=1 wbps=1 riops=1 wiops=1\n", ioDevice)
	return os.WriteFile(target, []byte(content), 0o644)
}

// HealIOStall restores disk read/write bandwidth and IOPS to max.
func (c *CgroupEngine) HealIOStall(cgroupPath, ioDevice string) error {
	if cgroupPath == "" || ioDevice == "" {
		return fmt.Errorf("cgroup: path and io_device cannot be empty")
	}
	target := filepath.Join(cgroupPath, "io.max")
	content := fmt.Sprintf("%s rbps=max wbps=max riops=max wiops=max\n", ioDevice)
	return os.WriteFile(target, []byte(content), 0o644)
}
