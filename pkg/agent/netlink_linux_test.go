//go:build linux

package agent

import (
	"bytes"
	"encoding/binary"
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
