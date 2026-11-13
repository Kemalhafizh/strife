package transport

import (
	"fmt"
	"syscall"
)

func formatDev(dev uint64) string {
	major := (dev>>8)&0xfff | (dev>>32)&^0xfff
	minor := dev&0xff | (dev>>12)&^0xff
	return fmt.Sprintf("%d:%d", major, minor)
}

// deviceOf returns the "major:minor" of the filesystem holding path.
func deviceOf(path string) (string, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return "", err
	}
	return formatDev(uint64(st.Dev)), nil //nolint:unconvert // Dev is uint32 on some arches
}

// blockDeviceOf returns the "major:minor" of a block device node in /dev.
func blockDeviceOf(path string) (string, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return "", err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFBLK {
		return "", fmt.Errorf("%s is not a block device", path)
	}
	return formatDev(uint64(st.Rdev)), nil //nolint:unconvert // Rdev is uint32 on some arches
}
