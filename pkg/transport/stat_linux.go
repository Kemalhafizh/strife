package transport

import (
	"fmt"
	"syscall"
)

// deviceOf returns the "major:minor" of the filesystem holding path.
func deviceOf(path string) (string, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return "", err
	}
	dev := uint64(st.Dev) //nolint:unconvert // Dev is uint32 on some arches
	major := (dev>>8)&0xfff | (dev>>32)&^0xfff
	minor := dev&0xff | (dev>>12)&^0xff
	return fmt.Sprintf("%d:%d", major, minor), nil
}
