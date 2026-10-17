package txn

import (
	"testing"

	"github.com/kemalhafizh/strife/internal/rng"
)

// FuzzCheckAppendSound derives small list-append histories from the fuzzer's
// bytes and checks the soundness property: a reported strict
// serializability violation must have no valid serial order.
func FuzzCheckAppendSound(f *testing.F) {
	for _, s := range []uint64{1, 2, 3, 42} {
		f.Add(s, uint8(4))
	}
	f.Fuzz(func(t *testing.T, seed uint64, n uint8) {
		r := rng.New(seed)
		ops, serial := randomAppendHistory(r, 2+int(n%4))
		rep := CheckAppend(ops, LevelStrictSerializable)
		if serial && rep.Verdict != Pass {
			t.Fatalf("serial history flagged:\n%s%s", dumpTxns(ops), rep.FullReport())
		}
		if rep.Verdict == Violation && bruteStrict(ops) {
			t.Fatalf("unsound verdict:\n%s%s", dumpTxns(ops), rep.FullReport())
		}
	})
}
