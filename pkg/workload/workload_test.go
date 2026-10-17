package workload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kemalhafizh/strife/internal/rng"
	"github.com/kemalhafizh/strife/pkg/history"
)

func TestPresetsCompileAndReplayDeterministically(t *testing.T) {
	for _, spec := range Presets() {
		t.Run(spec.Name, func(t *testing.T) {
			plug, err := Load("preset:" + spec.Name)
			if err != nil {
				t.Fatalf("Load(%s): %v", spec.Name, err)
			}
			if !IsWasmBinary(plug.WasmBytes) {
				t.Fatalf("expected valid wasm header for preset %s", spec.Name)
			}
			if len(plug.SHA256) != 64 {
				t.Fatalf("expected 64-char sha256 digest, got %q", plug.SHA256)
			}

			r1 := rng.New(424242)
			r2 := rng.New(424242)
			for step := 0; step < 200; step++ {
				op1, err := plug.NextOp(step%4, step, 10, 20, r1)
				if err != nil {
					t.Fatalf("step %d: %v", step, err)
				}
				op2, err := plug.NextOp(step%4, step, 10, 20, r2)
				if err != nil {
					t.Fatalf("step %d replay: %v", step, err)
				}
				if op1.String() != op2.String() {
					t.Fatalf("step %d determinism mismatch: %s vs %s", step, op1.String(), op2.String())
				}
			}
		})
	}
}

func TestReadHeavyAndHotspotDistributions(t *testing.T) {
	readHeavy, err := Load("preset:read-heavy")
	if err != nil {
		t.Fatal(err)
	}
	r := rng.New(9001)
	counts := map[string]int{}
	const total = 2000
	for i := 0; i < total; i++ {
		op, err := readHeavy.NextOp(0, i, 8, 16, r)
		if err != nil {
			t.Fatal(err)
		}
		counts[op.F]++
	}
	readRatio := float64(counts[history.FRead]) / float64(total)
	if readRatio < 0.78 || readRatio > 0.92 {
		t.Fatalf("read-heavy expected ~85%% reads, got %.2f%% (%v)", readRatio*100, counts)
	}

	hotspot, err := Load("preset:cas-hotspot")
	if err != nil {
		t.Fatal(err)
	}
	hotHits := 0
	for i := 0; i < total; i++ {
		op, err := hotspot.NextOp(i%3, i, 10, 10, r)
		if err != nil {
			t.Fatal(err)
		}
		if op.Key == "k0" || op.Key == "k1" {
			hotHits++
		}
	}
	hotRatio := float64(hotHits) / float64(total)
	if hotRatio < 0.75 {
		t.Fatalf("cas-hotspot expected >=75%% hits on hot 20%% keys (k0,k1), got %.2f%%", hotRatio*100)
	}
}

func TestSequentialDistribution(t *testing.T) {
	seq, err := Load("preset:sequential-scan")
	if err != nil {
		t.Fatal(err)
	}
	r := rng.New(77)
	for step := 0; step < 12; step++ {
		op, err := seq.NextOp(2, step, 5, 10, r)
		if err != nil {
			t.Fatal(err)
		}
		wantKey := "k" + string(rune('0'+((2+step)%5)))
		if op.Key != wantKey {
			t.Fatalf("step %d: got key %s, want %s", step, op.Key, wantKey)
		}
	}
}

func TestLoadWasmAndJSONFilesFromDisk(t *testing.T) {
	dir := t.TempDir()
	specJSON := `{
  "name": "queue-burst",
  "description": "Queue + counter custom mix",
  "distribution": "partitioned",
  "weights": {
    "enqueue": 40,
    "dequeue": 40,
    "inc": 10,
    "generate": 5,
    "add": 5
  }
}`
	jsonPath := filepath.Join(dir, "queue-burst.json")
	if err := os.WriteFile(jsonPath, []byte(specJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	plugFromJSON, err := Load(jsonPath)
	if err != nil {
		t.Fatalf("Load(json): %v", err)
	}
	if plugFromJSON.Source != "spec" || plugFromJSON.Name != "queue-burst" {
		t.Fatalf("unexpected plugin metadata: %+v", plugFromJSON)
	}

	wasmPath := filepath.Join(dir, "queue-burst.wasm")
	if err := os.WriteFile(wasmPath, plugFromJSON.WasmBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	plugFromWasm, err := Load(wasmPath)
	if err != nil {
		t.Fatalf("Load(wasm): %v", err)
	}
	if plugFromWasm.Source != "wasm" || plugFromWasm.SHA256 != plugFromJSON.SHA256 {
		t.Fatalf("wasm sha256 mismatch: %s vs %s", plugFromWasm.SHA256, plugFromJSON.SHA256)
	}

	r1 := rng.New(123)
	r2 := rng.New(123)
	for i := 0; i < 100; i++ {
		o1, err := plugFromJSON.NextOp(1, i, 4, 8, r1)
		if err != nil {
			t.Fatal(err)
		}
		o2, err := plugFromWasm.NextOp(1, i, 4, 8, r2)
		if err != nil {
			t.Fatal(err)
		}
		if o1.String() != o2.String() {
			t.Fatalf("json vs compiled wasm mismatch at %d: %s vs %s", i, o1.String(), o2.String())
		}
	}
}

func TestWasmFuelExhaustionTrap(t *testing.T) {
	// Build a minimal Wasm module whose `next_op` enters an infinite loop:
	// (loop (br 0)) (i64.const 0) (end)
	body := []byte{
		0x00, // 0 local declarations
		opLoop, blockTypeNil,
		opBr, 0x00,
		opEnd,
		opI64Const, 0x00,
		opEnd,
	}
	var codeSec []byte
	codeSec = appendU32(codeSec, 1)
	codeSec = appendU32(codeSec, uint32(len(body)))
	codeSec = append(codeSec, body...)

	modBytes := append([]byte{}, wasmMagic...)
	modBytes = appendSection(modBytes, secType, []byte{
		0x01, 0x60,
		0x05, valTypeI32, valTypeI32, valTypeI64, valTypeI32, valTypeI32,
		0x01, valTypeI64,
	})
	modBytes = appendSection(modBytes, secFunction, []byte{0x01, 0x00})
	modBytes = appendSection(modBytes, secExport, []byte{
		0x01, 0x07, 'n', 'e', 'x', 't', '_', 'o', 'p', 0x00, 0x00,
	})
	modBytes = appendSection(modBytes, secCode, codeSec)

	mod, err := ParseWasm(modBytes)
	if err != nil {
		t.Fatalf("ParseWasm: %v", err)
	}
	mod.SetMaxFuel(250)
	_, err = mod.CallNextOp(0, 0, 42, 4, 4)
	if err == nil || !strings.Contains(err.Error(), "fuel exhausted") {
		t.Fatalf("expected fuel exhausted error, got %v", err)
	}
}

func TestWasmMemoryAndHelperCall(t *testing.T) {
	// Build a module with:
	// - 1 memory page (64 KiB)
	// - helper func 0: `(i64) -> i64` that stores i64 at addr 0, loads it back, adds 1
	// - exported func 1 (`next_op`): calls func 0 with `i64.const 0` -> returns 1 (OpWrite on k0=0, val=0)
	fn0Body := []byte{
		0x00, // 0 locals
		opI32Const, 0x00,
		opLocalGet, 0x00,
		opI64Store, 0x03, 0x00,
		opI32Const, 0x00,
		opI64Load, 0x03, 0x00,
		opI64Const, 0x01,
		opI64Add,
		opEnd,
	}
	fn1Body := []byte{
		0x00, // 0 locals
		opI64Const, 0x00,
		opCall, 0x00,
		opEnd,
	}
	var codeSec []byte
	codeSec = appendU32(codeSec, 2)
	codeSec = appendU32(codeSec, uint32(len(fn0Body)))
	codeSec = append(codeSec, fn0Body...)
	codeSec = appendU32(codeSec, uint32(len(fn1Body)))
	codeSec = append(codeSec, fn1Body...)

	modBytes := append([]byte{}, wasmMagic...)
	modBytes = appendSection(modBytes, secType, []byte{
		0x02,
		0x60, 0x01, valTypeI64, 0x01, valTypeI64,
		0x60, 0x05, valTypeI32, valTypeI32, valTypeI64, valTypeI32, valTypeI32, 0x01, valTypeI64,
	})
	modBytes = appendSection(modBytes, secFunction, []byte{0x02, 0x00, 0x01})
	modBytes = appendSection(modBytes, secMemory, []byte{0x01, 0x00, 0x01})
	modBytes = appendSection(modBytes, secExport, []byte{
		0x01, 0x07, 'n', 'e', 'x', 't', '_', 'o', 'p', 0x00, 0x01,
	})
	modBytes = appendSection(modBytes, secCode, codeSec)

	mod, err := ParseWasm(modBytes)
	if err != nil {
		t.Fatalf("ParseWasm: %v", err)
	}
	got, err := mod.CallNextOp(0, 0, 99, 4, 4)
	if err != nil {
		t.Fatalf("CallNextOp: %v", err)
	}
	if got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
}
