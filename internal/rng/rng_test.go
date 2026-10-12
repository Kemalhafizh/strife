package rng

import "testing"

func TestDeterministic(t *testing.T) {
	a, b := New(42), New(42)
	for i := 0; i < 1000; i++ {
		if a.Uint64() != b.Uint64() {
			t.Fatalf("streams diverged at %d", i)
		}
	}
}

// TestGolden pins the output of the generator. If this test fails, every seed
// ever written into a report no longer replays: do not "fix" it by updating
// the constants.
func TestGolden(t *testing.T) {
	r := New(1)
	got := []uint64{r.Uint64(), r.Uint64(), r.Uint64()}
	want := goldenSeed1
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("value %d: got %#x want %#x", i, got[i], want[i])
		}
	}
}

func TestForkIndependentOfConsumption(t *testing.T) {
	a, b := New(7), New(7)
	for i := 0; i < 50; i++ {
		b.Uint64()
	}
	fa, fb := a.Fork("nemesis"), b.Fork("nemesis")
	for i := 0; i < 100; i++ {
		if fa.Uint64() != fb.Uint64() {
			t.Fatal("fork depends on parent consumption")
		}
	}
	if New(7).Fork("x").Uint64() == New(7).Fork("y").Uint64() {
		t.Fatal("different labels produced identical streams")
	}
}

func TestIntnRange(t *testing.T) {
	r := New(3)
	counts := make([]int, 7)
	for i := 0; i < 70000; i++ {
		v := r.Intn(7)
		if v < 0 || v >= 7 {
			t.Fatalf("out of range: %d", v)
		}
		counts[v]++
	}
	for i, c := range counts {
		if c < 9000 || c > 11000 {
			t.Fatalf("bucket %d badly skewed: %d", i, c)
		}
	}
}

func TestFloat64Range(t *testing.T) {
	r := New(9)
	for i := 0; i < 10000; i++ {
		if f := r.Float64(); f < 0 || f >= 1 {
			t.Fatalf("out of range: %v", f)
		}
	}
}

var goldenSeed1 = []uint64{0xb3f2af6d0fc710c5, 0x853b559647364cea, 0x92f89756082a4514}
