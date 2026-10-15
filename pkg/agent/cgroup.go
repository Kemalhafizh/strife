package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// CgroupEngine provides zero-overhead direct file writes to cgroup v2 controllers.
type CgroupEngine struct{}

// NewCgroupEngine creates a cgroup v2 manager.
func NewCgroupEngine() *CgroupEngine {
	return &CgroupEngine{}
}

// Available checks if cgroup v2 filesystem is accessible.
func (c *CgroupEngine) Available() bool {
	info, err := os.Stat("/sys/fs/cgroup")
	return err == nil && info.IsDir()
}

// Freeze sets cgroup.freeze to 1, suspending all processes in the cgroup.
func (c *CgroupEngine) Freeze(cgroupPath string) error {
	if cgroupPath == "" {
		return fmt.Errorf("cgroup: path cannot be empty")
	}
	target := filepath.Join(cgroupPath, "cgroup.freeze")
	return os.WriteFile(target, []byte("1\n"), 0o644)
}

// Unfreeze sets cgroup.freeze to 0, resuming all processes in the cgroup.
func (c *CgroupEngine) Unfreeze(cgroupPath string) error {
	if cgroupPath == "" {
		return fmt.Errorf("cgroup: path cannot be empty")
	}
	target := filepath.Join(cgroupPath, "cgroup.freeze")
	return os.WriteFile(target, []byte("0\n"), 0o644)
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
