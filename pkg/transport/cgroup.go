package transport

import (
	"context"
	"fmt"
	"os"
	"path"
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
		return path.Join(cgroupRoot, p), nil
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

// resolveIODevice resolves root to the whole-disk "major:minor" suitable for
// cgroup v2 io.max. When stat reports an anonymous superblock (major == 0,
// e.g. overlay or btrfs), mountInfo is consulted to locate the backing path or
// block device.
func resolveIODevice(root, mountInfo, sysRoot string, statDev, statBlock func(string) (string, error)) (string, error) {
	cur := root
	for range 5 {
		dev, err := statDev(cur)
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(dev, "0:") {
			return wholeDisk(sysRoot, dev)
		}
		entry, ok, err := findMountEntry(mountInfo, cur, dev)
		if err != nil {
			return "", fmt.Errorf("reading %s for %s (%s): %w", mountInfo, root, dev, err)
		}
		if !ok {
			return "", fmt.Errorf("block device %s for %s not found in %s", dev, root, mountInfo)
		}
		if entry.fsType == "overlay" {
			if upper, ok := overlayUpperDir(entry.options); ok && upper != "" {
				cur = upper
				continue
			}
		}
		src := entry.source
		if i := strings.IndexByte(src, '['); i > 0 {
			src = src[:i]
		}
		if src != "" {
			if bdev, err := statBlock(src); err == nil && !strings.HasPrefix(bdev, "0:") {
				return wholeDisk(sysRoot, bdev)
			}
		}
		return "", fmt.Errorf("cannot resolve physical block device for %s (filesystem %s, device %s)", root, entry.fsType, dev)
	}
	return "", fmt.Errorf("too many nested mounts resolving disk for %s", root)
}

type mountEntry struct {
	mountPoint string
	fsType     string
	source     string
	options    string
}

func findMountEntry(mountInfo, target, dev string) (mountEntry, bool, error) {
	data, err := os.ReadFile(mountInfo)
	if err != nil {
		return mountEntry{}, false, err
	}
	var best, fallback mountEntry
	bestLen := -1
	hasFallback := false
	cleanTarget := path.Clean(target)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		left := strings.Fields(before)
		if len(left) < 5 || left[2] != dev {
			continue
		}
		right := strings.SplitN(after, " ", 3)
		if len(right) < 3 {
			continue
		}
		mp := path.Clean(unescapeMountField(left[4]))
		e := mountEntry{
			mountPoint: mp,
			fsType:     right[0],
			source:     unescapeMountField(right[1]),
			options:    unescapeMountField(right[2]),
		}
		fallback = e
		hasFallback = true
		if mp == "/" && len(mp) > bestLen || cleanTarget == mp || strings.HasPrefix(cleanTarget, mp+"/") {
			if len(mp) >= bestLen {
				best = e
				bestLen = len(mp)
			}
		}
	}
	if bestLen >= 0 {
		return best, true, nil
	}
	return fallback, hasFallback, nil
}

func overlayUpperDir(opts string) (string, bool) {
	for _, opt := range strings.Split(opts, ",") {
		if upper, ok := strings.CutPrefix(opt, "upperdir="); ok {
			return upper, true
		}
	}
	return "", false
}

func unescapeMountField(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	isOctal := func(b byte) bool { return b >= '0' && b <= '7' }
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
