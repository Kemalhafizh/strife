package workload

import "testing"

// FuzzParseWasm feeds arbitrary bytes to the Wasm decoder and runs whatever
// it accepts: decoding must fail cleanly, and execution must stay within its
// fuel and call-depth limits (a trap, never a hang or a panic).
func FuzzParseWasm(f *testing.F) {
	for _, p := range Presets() {
		if b, err := CompileSpecToWasm(p); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte("\x00asm\x01\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := ParseWasm(data)
		if err != nil {
			return
		}
		m.SetMaxFuel(2000)
		for step := int32(0); step < 3; step++ {
			_, _ = m.CallNextOp(1, step, 0x9e3779b97f4a7c15, 4, 5)
		}
	})
}
