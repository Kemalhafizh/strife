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

	tcHRoot   = 0xFFFFFFFF // TC_H_ROOT
	tcaKind   = 1          // TCA_KIND
	tcaOptions = 2         // TCA_OPTIONS
)

// tcNetemQopt matches Linux kernel struct tc_netem_qopt.
type tcNetemQopt struct {
	Latency   uint32 // in microseconds
	Jitter    uint32 // in microseconds
	Limit     uint32 // FIFO queue length limit
	Loss      uint32 // random packet loss (0 to 2^32 - 1)
	Gap       uint32 // packet reordering gap
	Duplicate uint32 // packet duplication rate
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
	return n.setNetemTC(iface, delay, jitter, loss)
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

	qopt := tcNetemQopt{
		Latency: uint32(delay.Microseconds()),
		Jitter:  uint32(jitter.Microseconds()),
		Limit:   1000,
	}
	if loss > 0 {
		qopt.Loss = uint32(loss * float64(^uint32(0)))
	}

	var optBuf bytes.Buffer
	if err := binary.Write(&optBuf, binary.LittleEndian, qopt); err != nil {
		return err
	}

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
		return nil // Non-fatal on delete
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
