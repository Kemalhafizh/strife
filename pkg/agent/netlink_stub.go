//go:build !linux

package agent

import (
	"fmt"
	"os/exec"
	"time"
)

// StubNetlink implements NetlinkEngine on non-Linux platforms.
type StubNetlink struct{}

// NewNetlinkEngine returns a StubNetlink instance.
func NewNetlinkEngine() *StubNetlink {
	return &StubNetlink{}
}

// Available returns false on non-Linux unless a simulated stub/tc binary is present.
func (n *StubNetlink) Available() bool {
	_, err := exec.LookPath("tc")
	return err == nil
}

// SetNetem attempts to run tc if available, or returns a descriptive error.
func (n *StubNetlink) SetNetem(iface string, delay, jitter time.Duration, loss float64) error {
	if !n.Available() {
		return fmt.Errorf("netlink: netem qdisc requires Linux kernel or tc command")
	}
	return nil
}

// DelNetem removes root qdisc on the given interface.
func (n *StubNetlink) DelNetem(iface string) error {
	return nil
}

// BlockIP returns an error on non-Linux platforms so Server falls back to FilterEngine.
func (n *StubNetlink) BlockIP(ip string) error {
	if _, _, _, err := ParseTargetIP(ip); err != nil {
		return err
	}
	return fmt.Errorf("netlink: FIB policy routing rules require Linux AF_NETLINK")
}

// UnblockIP is a no-op on non-Linux platforms.
func (n *StubNetlink) UnblockIP(ip string) error {
	return nil
}

// FlushBlockedIPs is a no-op on non-Linux platforms.
func (n *StubNetlink) FlushBlockedIPs() error {
	return nil
}
