//go:build linux

package agent

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"os/exec"
	"syscall"
	"time"
	"unsafe"
)

// Linux Netlink RTNETLINK constants
const (
	rtmNewQdisc = 36 // RTM_NEWQDISC
	rtmDelQdisc = 37 // RTM_DELQDISC

	nlmFRequest = 0x01 // NLM_F_REQUEST
	nlmFAck     = 0x04 // NLM_F_ACK
	nlmFCreate  = 0x400
	nlmFReplace = 0x100

	tcHRoot    = 0xFFFFFFFF // TC_H_ROOT
	tcaKind    = 1          // TCA_KIND
	tcaOptions = 2          // TCA_OPTIONS
)

// tcNetemQopt matches the kernel's struct tc_netem_qopt, field for field
// (include/uapi/linux/pkt_sched.h). Latency and jitter are in psched
// ticks, which are nanoseconds on every kernel since 2.6.3x; the exact
// 64-bit values also go in TCA_NETEM_LATENCY64/JITTER64.
type tcNetemQopt struct {
	Latency   uint32 // ticks (ns)
	Limit     uint32 // queue length in packets; 0 would drop everything
	Loss      uint32 // random loss probability, 0..2^32-1
	Gap       uint32 // re-ordering gap, 0 = none
	Duplicate uint32 // duplication probability
	Jitter    uint32 // ticks (ns)
}

// Netem attributes that follow the qopt struct inside TCA_OPTIONS.
const (
	tcaNetemLatency64 = 10
	tcaNetemJitter64  = 11
)

// netlinkTimeout bounds how long the agent waits for the kernel's ack.
var netlinkTimeout = syscall.Timeval{Sec: 2}

func clampU32(ns int64) uint32 {
	if ns < 0 {
		return 0
	}
	if ns > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(ns)
}

// LinuxNetlink implements NetlinkEngine for Linux.
type LinuxNetlink struct{}

// NewNetlinkEngine returns a LinuxNetlink instance.
func NewNetlinkEngine() *LinuxNetlink {
	return &LinuxNetlink{}
}

// Available checks if RTNETLINK socket or tc is usable.
func (n *LinuxNetlink) Available() bool {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW, syscall.NETLINK_ROUTE)
	if err == nil {
		syscall.Close(fd)
		return true
	}
	_, err = exec.LookPath("tc")
	return err == nil
}

// SetNetem adds or replaces a netem qdisc on the given interface.
func (n *LinuxNetlink) SetNetem(iface string, delay, jitter time.Duration, loss float64) error {
	// First attempt via direct kernel Netlink socket.
	err := n.setNetemNetlink(iface, delay, jitter, loss)
	if err == nil {
		return nil
	}
	// Fall back to local tc execution if netlink failed.
	if tcErr := n.setNetemTC(iface, delay, jitter, loss); tcErr != nil {
		return fmt.Errorf("%w (tc fallback: %v)", err, tcErr)
	}
	return nil
}

// DelNetem removes root qdisc on the given interface.
func (n *LinuxNetlink) DelNetem(iface string) error {
	err := n.delNetemNetlink(iface)
	if err == nil {
		return nil
	}
	return n.delNetemTC(iface)
}

func (n *LinuxNetlink) setNetemNetlink(iface string, delay, jitter time.Duration, loss float64) error {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return fmt.Errorf("netlink: interface %s: %w", iface, err)
	}

	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW, syscall.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &netlinkTimeout); err != nil {
		return err
	}

	qopt := tcNetemQopt{
		Latency: clampU32(delay.Nanoseconds()),
		Jitter:  clampU32(jitter.Nanoseconds()),
		Limit:   1000,
	}
	if loss > 0 {
		qopt.Loss = uint32(min(loss, 1) * float64(^uint32(0)))
	}

	var optBuf bytes.Buffer
	if err := binary.Write(&optBuf, binary.LittleEndian, qopt); err != nil {
		return err
	}
	writeAttr(&optBuf, tcaNetemLatency64, binary.LittleEndian.AppendUint64(nil, uint64(delay.Nanoseconds())))
	writeAttr(&optBuf, tcaNetemJitter64, binary.LittleEndian.AppendUint64(nil, uint64(jitter.Nanoseconds())))

	var attrBuf bytes.Buffer
	writeAttr(&attrBuf, tcaKind, append([]byte("netem"), 0))
	writeAttr(&attrBuf, tcaOptions, optBuf.Bytes())

	tcHdr := struct {
		Family  uint8
		Pad1    uint8
		Pad2    uint16
		Ifindex int32
		Handle  uint32
		Parent  uint32
		Info    uint32
	}{
		Family:  0, // AF_UNSPEC
		Ifindex: int32(ifi.Index),
		Handle:  0xFFFF0000,
		Parent:  tcHRoot,
		Info:    0,
	}

	var tcBuf bytes.Buffer
	if err := binary.Write(&tcBuf, binary.LittleEndian, tcHdr); err != nil {
		return err
	}
	tcBuf.Write(attrBuf.Bytes())

	nlHdr := syscall.NlMsghdr{
		Len:   uint32(syscall.SizeofNlMsghdr + tcBuf.Len()),
		Type:  rtmNewQdisc,
		Flags: nlmFRequest | nlmFAck | nlmFCreate | nlmFReplace,
		Seq:   uint32(time.Now().UnixNano()),
		Pid:   uint32(syscall.Getpid()),
	}

	var msgBuf bytes.Buffer
	var hdrBytes [syscall.SizeofNlMsghdr]byte
	*(*syscall.NlMsghdr)(unsafe.Pointer(&hdrBytes[0])) = nlHdr
	msgBuf.Write(hdrBytes[:])
	msgBuf.Write(tcBuf.Bytes())

	sa := &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}
	if err := syscall.Sendto(fd, msgBuf.Bytes(), 0, sa); err != nil {
		return err
	}

	rb := make([]byte, 4096)
	nr, _, err := syscall.Recvfrom(fd, rb, 0)
	if err != nil {
		return err
	}
	if nr < syscall.SizeofNlMsghdr {
		return fmt.Errorf("netlink: short reply (%d bytes)", nr)
	}
	respHdr := (*syscall.NlMsghdr)(unsafe.Pointer(&rb[0]))
	if respHdr.Type == syscall.NLMSG_ERROR {
		if nr >= syscall.SizeofNlMsghdr+4 {
			errno := -int32(binary.LittleEndian.Uint32(rb[syscall.SizeofNlMsghdr : syscall.SizeofNlMsghdr+4]))
			if errno != 0 {
				return syscall.Errno(errno)
			}
		}
	}
	return nil
}

func (n *LinuxNetlink) delNetemNetlink(iface string) error {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return fmt.Errorf("netlink: interface %s: %w", iface, err)
	}

	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW, syscall.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &netlinkTimeout); err != nil {
		return err
	}

	tcHdr := struct {
		Family  uint8
		Pad1    uint8
		Pad2    uint16
		Ifindex int32
		Handle  uint32
		Parent  uint32
		Info    uint32
	}{
		Family:  0,
		Ifindex: int32(ifi.Index),
		Handle:  0,
		Parent:  tcHRoot,
		Info:    0,
	}

	var tcBuf bytes.Buffer
	if err := binary.Write(&tcBuf, binary.LittleEndian, tcHdr); err != nil {
		return err
	}

	nlHdr := syscall.NlMsghdr{
		Len:   uint32(syscall.SizeofNlMsghdr + tcBuf.Len()),
		Type:  rtmDelQdisc,
		Flags: nlmFRequest | nlmFAck,
		Seq:   uint32(time.Now().UnixNano()),
		Pid:   uint32(syscall.Getpid()),
	}

	var msgBuf bytes.Buffer
	var hdrBytes [syscall.SizeofNlMsghdr]byte
	*(*syscall.NlMsghdr)(unsafe.Pointer(&hdrBytes[0])) = nlHdr
	msgBuf.Write(hdrBytes[:])
	msgBuf.Write(tcBuf.Bytes())

	sa := &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}
	if err := syscall.Sendto(fd, msgBuf.Bytes(), 0, sa); err != nil {
		return err
	}

	rb := make([]byte, 4096)
	nr, _, err := syscall.Recvfrom(fd, rb, 0)
	if err != nil {
		return fmt.Errorf("netlink: no ack for qdisc delete: %w", err)
	}
	if nr >= syscall.SizeofNlMsghdr+4 {
		respHdr := (*syscall.NlMsghdr)(unsafe.Pointer(&rb[0]))
		if respHdr.Type == syscall.NLMSG_ERROR {
			errno := -int32(binary.LittleEndian.Uint32(rb[syscall.SizeofNlMsghdr : syscall.SizeofNlMsghdr+4]))
			if errno != 0 && errno != int32(syscall.ENOENT) {
				return syscall.Errno(errno)
			}
		}
	}
	return nil
}

func writeAttr(buf *bytes.Buffer, attrType uint16, data []byte) {
	attrLen := uint16(4 + len(data))
	pad := (4 - (len(data) % 4)) % 4

	binary.Write(buf, binary.LittleEndian, attrLen)
	binary.Write(buf, binary.LittleEndian, attrType)
	buf.Write(data)
	if pad > 0 {
		buf.Write(make([]byte, pad))
	}
}

func (n *LinuxNetlink) setNetemTC(iface string, delay, jitter time.Duration, loss float64) error {
	var args []string
	args = append(args, "qdisc", "replace", "dev", iface, "root", "netem")
	if delay > 0 {
		args = append(args, "delay", fmt.Sprintf("%dms", delay.Milliseconds()))
		if jitter > 0 {
			args = append(args, fmt.Sprintf("%dms", jitter.Milliseconds()), "distribution", "normal")
		}
	}
	if loss > 0 {
		args = append(args, "loss", fmt.Sprintf("%.2f%%", loss*100))
	}
	cmd := exec.Command("tc", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tc set: %w: %s", err, string(out))
	}
	return nil
}

func (n *LinuxNetlink) delNetemTC(iface string) error {
	cmd := exec.Command("tc", "qdisc", "del", "dev", iface, "root")
	_ = cmd.Run() // Ignore errors if already deleted
	return nil
}
