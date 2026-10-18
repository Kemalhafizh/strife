package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// diskOf returns the "major:minor" of the whole disk holding path: the
// device of its filesystem, or the disk a partition belongs to, since
// io.max only takes whole disks.
func diskOf(sys, path string) (string, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return "", err
	}
	dev := uint64(st.Dev) //nolint:unconvert // Dev is uint32 on some arches
	major := (dev>>8)&0xfff | (dev>>32)&^0xfff
	minor := dev&0xff | (dev>>12)&^0xff
	mm := fmt.Sprintf("%d:%d", major, minor)
	block := filepath.Join(sys, "dev", "block", mm)
	if _, err := os.Stat(block); err != nil {
		return "", fmt.Errorf("agent: %s is on %s, which is not a block device", path, mm)
	}
	if _, err := os.Stat(filepath.Join(block, "partition")); err != nil {
		return mm, nil
	}
	real, err := filepath.EvalSymlinks(block)
	if err != nil {
		return "", err
	}
	parent, err := os.ReadFile(filepath.Join(filepath.Dir(real), "dev"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(parent)), nil
}
