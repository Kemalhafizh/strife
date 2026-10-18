//go:build linux

package agent

import (
	"bytes"
	"encoding/binary"
	"net"
	"syscall"
	"testing"
	"unsafe"
)

// The struct must match the kernel's tc_netem_qopt byte for byte: getting
// the order wrong once made a loss fault set the queue limit to zero, which
// drops every packet.
func TestNetemQoptLayout(t *testing.T) {
	if unsafe.Sizeof(tcNetemQopt{}) != 24 {
		t.Fatalf("size %d, want 24", unsafe.Sizeof(tcNetemQopt{}))
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, tcNetemQopt{Latency: 1, Limit: 2, Loss: 3, Gap: 4, Duplicate: 5, Jitter: 6})
	want := []uint32{1, 2, 3, 4, 5, 6} // latency, limit, loss, gap, duplicate, jitter
	for i, w := range want {
		if got := binary.LittleEndian.Uint32(b.Bytes()[4*i:]); got != w {
			t.Fatalf("field %d = %d, want %d", i, got, w)
		}
	}
	if clampU32(50_000_000) != 50_000_000 || clampU32(-1) != 0 || clampU32(1<<40) != ^uint32(0) {
		t.Fatal("clampU32")
	}
}

// qdiscRequest sends one root qdisc request for ifindex and returns the
// kernel's answer.
func qdiscRequest(typ, flags uint16, ifindex int, kind string) error {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW, syscall.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var body bytes.Buffer
	_ = binary.Write(&body, binary.LittleEndian, struct {
		Family               uint8
		Pad1                 uint8
		Pad2                 uint16
		Ifindex              int32
		Handle, Parent, Info uint32
	}{Ifindex: int32(ifindex), Handle: 0x10000, Parent: tcHRoot})
	writeAttr(&body, tcaKind, append([]byte(kind), 0))
	hdr := syscall.NlMsghdr{Len: uint32(syscall.SizeofNlMsghdr + body.Len()), Type: typ, Flags: nlmFRequest | nlmFAck | flags, Seq: 1}
	msg := append((*[syscall.SizeofNlMsghdr]byte)(unsafe.Pointer(&hdr))[:], body.Bytes()...)
	if err := syscall.Sendto(fd, msg, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return err
	}
	rb := make([]byte, 4096)
	n, _, err := syscall.Recvfrom(fd, rb, 0)
	if err != nil {
		return err
	}
	if n >= syscall.SizeofNlMsghdr+4 && (*syscall.NlMsghdr)(unsafe.Pointer(&rb[0])).Type == syscall.NLMSG_ERROR {
		if errno := -int32(binary.LittleEndian.Uint32(rb[syscall.SizeofNlMsghdr:])); errno != 0 {
			return syscall.Errno(errno)
		}
	}
	return nil
}

// Healing a latency or loss fault removes the agent's netem, never a root
// qdisc someone else installed (a CNI's bandwidth shaping, say): teardown
// runs on every agent shutdown.
func TestDelNetemSparesOtherQdiscs(t *testing.T) {
	inFreshNetns(t, func() string {
		lo, err := net.InterfaceByName("lo")
		if err != nil {
			t.Error(err)
			return ""
		}
		if err := qdiscRequest(rtmNewQdisc, nlmFCreate, lo.Index, "pfifo"); err != nil {
			return "cannot install a pfifo root qdisc: " + err.Error()
		}
		if err := NewNetlinkEngine().DelNetem("lo"); err != nil {
			t.Errorf("DelNetem with a pfifo root: %v", err)
		}
		// Deleting it by kind succeeds only if DelNetem left it there.
		if err := qdiscRequest(rtmDelQdisc, 0, lo.Index, "pfifo"); err != nil {
			t.Errorf("the pfifo root qdisc is gone after DelNetem: %v", err)
		}
		if err := NewNetlinkEngine().DelNetem("lo"); err != nil {
			t.Errorf("DelNetem with only the default qdisc: %v", err)
		}
		return ""
	})
}
