package transport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Auto, as a node's cgroup or as nemesis.io_device, asks the transport to
// discover the value when the run starts.
const Auto = "auto"

// HostResolver is implemented by transports that can discover, on the
// machine hosting a node, what the cgroup faults need.
type HostResolver interface {
	// ResolveCgroup returns the absolute cgroup v2 directory holding the
	// node's processes.
	ResolveCgroup(ctx context.Context, n Node) (string, error)
	// ResolveIODevice returns the "major:minor" of the whole disk that holds
	// the nodes' data.
	ResolveIODevice(ctx context.Context) (string, error)
}

// cgroupOfPID reads /proc/PID/cgroup and returns the cgroup v2 directory of
// the process under root (normally /sys/fs/cgroup).
func cgroupOfPID(procRoot, cgroupRoot string, pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, fmt.Sprint(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	// A pure cgroup v2 host has a single "0::/path" line; v1 and hybrid
	// hosts list one line per controller hierarchy.
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if p, ok := strings.CutPrefix(lines[0], "0::"); ok && len(lines) == 1 {
		return filepath.Join(cgroupRoot, p), nil
	}
	return "", fmt.Errorf("pid %d is not on a pure cgroup v2 hierarchy (freeze and io-stall need cgroup v2)", pid)
}

// wholeDisk maps a block device "major:minor" to the disk that contains it:
// io.max only accepts whole disks, never partitions.
func wholeDisk(sysRoot, dev string) (string, error) {
	link := filepath.Join(sysRoot, "dev", "block", dev)
	if _, err := os.Stat(filepath.Join(link, "partition")); err != nil {
		if _, err := os.Stat(link); err != nil {
			return "", fmt.Errorf("block device %s not found in %s", dev, filepath.Dir(link))
		}
		return dev, nil
	}
	real, err := filepath.EvalSymlinks(link)
	if err != nil {
		return "", err
	}
	parent, err := os.ReadFile(filepath.Join(filepath.Dir(real), "dev"))
	if err != nil {
		return "", fmt.Errorf("parent disk of %s: %w", dev, err)
	}
	return strings.TrimSpace(string(parent)), nil
}
