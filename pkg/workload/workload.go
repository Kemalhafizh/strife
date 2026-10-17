package workload

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kemalhafizh/strife/internal/rng"
	"github.com/kemalhafizh/strife/pkg/history"
)

// Operation kind codes returned in the low 8 bits of `next_op`.
const (
	OpRead     uint8 = 0
	OpWrite    uint8 = 1
	OpCas      uint8 = 2
	OpAdd      uint8 = 3
	OpEnqueue  uint8 = 4
	OpDequeue  uint8 = 5
	OpInc      uint8 = 6
	OpGenerate uint8 = 7
	OpTxn      uint8 = 8
)

// Key distribution modes supported by declarative specs and the Wasm compiler.
const (
	DistUniform     = "uniform"
	DistHotspot     = "hotspot"
	DistSequential  = "sequential"
	DistPartitioned = "partitioned"
)

// Weights defines relative integer weights for each operation kind.
type Weights struct {
	Read     int `json:"read,omitempty"`
	Write    int `json:"write,omitempty"`
	Cas      int `json:"cas,omitempty"`
	Add      int `json:"add,omitempty"`
	Enqueue  int `json:"enqueue,omitempty"`
	Dequeue  int `json:"dequeue,omitempty"`
	Inc      int `json:"inc,omitempty"`
	Generate int `json:"generate,omitempty"`
	Txn      int `json:"txn,omitempty"`
}

// Total returns the sum of all positive weights.
func (w Weights) Total() int {
	return max(0, w.Read) + max(0, w.Write) + max(0, w.Cas) +
		max(0, w.Add) + max(0, w.Enqueue) + max(0, w.Dequeue) +
		max(0, w.Inc) + max(0, w.Generate) + max(0, w.Txn)
}

// Spec is a declarative workload specification that can be loaded directly or
// compiled into a standalone WebAssembly 1.0 (.wasm) plugin binary.
type Spec struct {
	Name              string  `json:"name"`
	Description       string  `json:"description,omitempty"`
	Distribution      string  `json:"distribution,omitempty"`
	HotspotKeyPct     int     `json:"hotspot_key_pct,omitempty"`
	HotspotTrafficPct int     `json:"hotspot_traffic_pct,omitempty"`
	Weights           Weights `json:"weights"`
}

// Defaults fills unset fields with sensible defaults.
func (s *Spec) Defaults() {
	if s.Name == "" {
		s.Name = "custom-workload"
	}
	if s.Distribution == "" {
		s.Distribution = DistUniform
	}
	if s.Distribution == DistHotspot {
		if s.HotspotKeyPct <= 0 || s.HotspotKeyPct >= 100 {
			s.HotspotKeyPct = 20
		}
		if s.HotspotTrafficPct <= 0 || s.HotspotTrafficPct > 100 {
			s.HotspotTrafficPct = 80
		}
	}
}

// Validate checks that the workload spec is well-formed.
func (s *Spec) Validate() error {
	s.Defaults()
	switch s.Distribution {
	case DistUniform, DistHotspot, DistSequential, DistPartitioned:
	default:
		return fmt.Errorf("workload: unknown distribution %q (expected uniform, hotspot, sequential, or partitioned)", s.Distribution)
	}
	if s.Weights.Total() <= 0 {
		return fmt.Errorf("workload: weights must have a positive total")
	}
	return nil
}

// PackOp encodes an operation choice into the 64-bit integer ABI returned by
// Wasm `next_op`:
//   - bits 0..7:   operation code (OpRead..OpTxn)
//   - bits 8..23:  key index (uint16)
//   - bits 24..43: primary value (20-bit unsigned integer)
//   - bits 44..63: auxiliary/expected value (20-bit unsigned integer)
func PackOp(code uint8, keyIdx uint16, value, auxValue int64) uint64 {
	v := uint64(value) & 0xFFFFF
	aux := uint64(auxValue) & 0xFFFFF
	return uint64(code) | (uint64(keyIdx) << 8) | (v << 24) | (aux << 44)
}

// UnpackOp decodes the 64-bit return value of `next_op` into a history.Op.
func UnpackOp(packed uint64, process, keys, values int) history.Op {
	if keys < 1 {
		keys = 1
	}
	if values < 1 {
		values = 1
	}
	code := uint8(packed & 0xFF)
	rawKey := int((packed >> 8) & 0xFFFF)
	rawVal := int64((packed >> 24) & 0xFFFFF)
	rawAux := int64((packed >> 44) & 0xFFFFF)

	kIdx := rawKey % keys
	val := rawVal % int64(values)
	aux := rawAux % int64(values)

	op := history.Op{
		Process: process,
		Type:    history.Invoke,
	}
	switch code {
	case OpRead:
		op.F = history.FRead
		op.Key = fmt.Sprintf("k%d", kIdx)
	case OpWrite:
		op.F = history.FWrite
		op.Key = fmt.Sprintf("k%d", kIdx)
		op.Value = history.Int(val)
	case OpCas:
		op.F = history.FCas
		op.Key = fmt.Sprintf("k%d", kIdx)
		op.Old = history.Int(aux)
		op.Value = history.Int(val)
	case OpAdd:
		op.F = history.FAdd
		op.Key = "s0"
		op.Value = history.Int(rawVal%1000 + int64(process)*1000)
	case OpEnqueue:
		op.F = history.FEnqueue
		op.Key = fmt.Sprintf("q%d", kIdx)
		op.Value = history.Int(val)
	case OpDequeue:
		op.F = history.FDequeue
		op.Key = fmt.Sprintf("q%d", kIdx)
	case OpInc:
		op.F = history.FInc
		op.Key = fmt.Sprintf("c%d", kIdx)
		op.Value = history.Int(1 + (val % 5))
	case OpGenerate:
		op.F = history.FGenerate
		op.Key = "uid"
	case OpTxn:
		op.F = history.FTxn
		kA := fmt.Sprintf("k%d", kIdx)
		kB := fmt.Sprintf("k%d", (kIdx+1+int(rawAux))%keys)
		op.Txn = []history.MicroOp{
			{F: "r", Key: kA},
			{F: "r", Key: kB},
			{F: "w", Key: kA, Value: history.Int(val)},
		}
	default:
		op.F = history.FRead
		op.Key = fmt.Sprintf("k%d", kIdx)
	}
	return op
}

// Presets returns all built-in enterprise workload specifications.
func Presets() []Spec {
	return []Spec{
		{
			Name:         "kv-balanced",
			Description:  "Balanced linearizable KV mix (50% read, 30% write, 20% CAS) with uniform key access",
			Distribution: DistUniform,
			Weights:      Weights{Read: 50, Write: 30, Cas: 20},
		},
		{
			Name:         "read-heavy",
			Description:  "YCSB-B style read-intensive KV workload (85% read, 10% write, 5% CAS)",
			Distribution: DistUniform,
			Weights:      Weights{Read: 85, Write: 10, Cas: 5},
		},
		{
			Name:         "write-heavy",
			Description:  "YCSB-A style update-intensive KV workload (20% read, 60% write, 20% CAS)",
			Distribution: DistUniform,
			Weights:      Weights{Read: 20, Write: 60, Cas: 20},
		},
		{
			Name:              "cas-hotspot",
			Description:       "High-contention compare-and-swap hotspot (80% traffic concentrated on 20% hot keys)",
			Distribution:      DistHotspot,
			HotspotKeyPct:     20,
			HotspotTrafficPct: 80,
			Weights:           Weights{Read: 25, Write: 25, Cas: 50},
		},
		{
			Name:         "sequential-scan",
			Description:  "Sequential key traversal across processes and steps (70% read, 30% write)",
			Distribution: DistSequential,
			Weights:      Weights{Read: 70, Write: 30},
		},
		{
			Name:              "si-write-skew",
			Description:       "Multi-key read-then-write transactional workload stressing Snapshot Isolation write skew",
			Distribution:      DistHotspot,
			HotspotKeyPct:     25,
			HotspotTrafficPct: 85,
			Weights:           Weights{Txn: 100},
		},
	}
}

// Preset looks up a built-in workload specification by name.
func Preset(name string) (Spec, bool) {
	clean := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "preset:")
	for _, p := range Presets() {
		if p.Name == clean {
			return p, true
		}
	}
	return Spec{}, false
}

// CompileSpecToWasm compiles a declarative Spec into a standalone, deterministic
// WebAssembly 1.0 (.wasm) binary exporting `next_op(i32, i32, i64, i32, i32) -> i64`.
func CompileSpecToWasm(spec Spec) ([]byte, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	type weightedOp struct {
		code   uint8
		weight int
	}
	candidates := []weightedOp{
		{OpRead, spec.Weights.Read},
		{OpWrite, spec.Weights.Write},
		{OpCas, spec.Weights.Cas},
		{OpAdd, spec.Weights.Add},
		{OpEnqueue, spec.Weights.Enqueue},
		{OpDequeue, spec.Weights.Dequeue},
		{OpInc, spec.Weights.Inc},
		{OpGenerate, spec.Weights.Generate},
		{OpTxn, spec.Weights.Txn},
	}
	var active []weightedOp
	totalWeight := 0
	for _, c := range candidates {
		if c.weight > 0 {
			totalWeight += c.weight
			active = append(active, c)
		}
	}

	// Function parameters:
	//   local 0: process (i32)
	//   local 1: step    (i32)
	//   local 2: rand    (i64)
	//   local 3: keys    (i32)
	//   local 4: values  (i32)
	// Local variables:
	//   local 5: rOp     (i32)
	//   local 6: opCode  (i64)
	//   local 7: keyIdx  (i32)
	//   local 8: val     (i64)
	//   local 9: aux     (i64)
	//   local 10: hotN   (i32)
	var code []byte
	// Declare locals: 3x i32 (locals 5, 7, 10 -> wait: let's group by type cleanly:
	// locals 5, 6, 7 = i32 (rOp, keyIdx, hotN), locals 8, 9, 10 = i64 (opCode, val, aux))
	const (
		locProcess = 0
		locStep    = 1
		locRand    = 2
		locKeys    = 3
		locValues  = 4
		locROp     = 5  // i32
		locKeyIdx  = 6  // i32
		locHotN    = 7  // i32
		locOpCode  = 8  // i64
		locVal     = 9  // i64
		locAux     = 10 // i64
	)

	var body []byte
	// rOp = i32.wrap_i64(rand & 0xFFFF) % totalWeight
	body = append(body, opLocalGet, locRand)
	body = append(body, opI32WrapI64)
	body = append(body, opI32Const)
	body = appendS64(body, 0xFFFF)
	body = append(body, opI32And)
	body = append(body, opI32Const)
	body = appendS64(body, int64(totalWeight))
	body = append(body, opI32RemU)
	body = append(body, opLocalSet, locROp)

	// Default opCode = last active op
	lastCode := active[len(active)-1].code
	body = append(body, opI64Const)
	body = appendS64(body, int64(lastCode))
	body = append(body, opLocalSet, locOpCode)

	// Cumulative threshold selection (evaluated in reverse so the first matching lower bound wins)
	cum := totalWeight
	for i := len(active) - 1; i >= 0; i-- {
		cum -= active[i].weight
		if i == len(active)-1 {
			continue
		}
		threshold := cum + active[i].weight
		body = append(body, opLocalGet, locROp)
		body = append(body, opI32Const)
		body = appendS64(body, int64(threshold))
		body = append(body, opI32LtU)
		body = append(body, opIf, blockTypeNil)
		body = append(body, opI64Const)
		body = appendS64(body, int64(active[i].code))
		body = append(body, opLocalSet, locOpCode)
		body = append(body, opEnd)
	}

	// Compute keyIdx according to distribution
	switch spec.Distribution {
	case DistSequential:
		// keyIdx = (process + step) % keys
		body = append(body, opLocalGet, locProcess)
		body = append(body, opLocalGet, locStep)
		body = append(body, opI32Add)
		body = append(body, opLocalGet, locKeys)
		body = append(body, opI32RemU)
		body = append(body, opLocalSet, locKeyIdx)

	case DistPartitioned:
		// keyIdx = (process * 131 + ((rand >> 16) & 0xFFFF)) % keys
		body = append(body, opLocalGet, locProcess)
		body = append(body, opI32Const)
		body = appendS64(body, 131)
		body = append(body, opI32Mul)
		body = append(body, opLocalGet, locRand)
		body = append(body, opI64Const, 16)
		body = append(body, opI64ShrU)
		body = append(body, opI32WrapI64)
		body = append(body, opI32Const)
		body = appendS64(body, 0xFFFF)
		body = append(body, opI32And)
		body = append(body, opI32Add)
		body = append(body, opLocalGet, locKeys)
		body = append(body, opI32RemU)
		body = append(body, opLocalSet, locKeyIdx)

	case DistHotspot:
		// hotN = (keys * HotspotKeyPct) / 100; if hotN == 0 { hotN = 1 }
		body = append(body, opLocalGet, locKeys)
		body = append(body, opI32Const)
		body = appendS64(body, int64(spec.HotspotKeyPct))
		body = append(body, opI32Mul)
		body = append(body, opI32Const)
		body = appendS64(body, 100)
		body = append(body, opI32DivU)
		body = append(body, opLocalTee, locHotN)
		body = append(body, opI32Eqz)
		body = append(body, opIf, blockTypeNil)
		body = append(body, opI32Const, 1)
		body = append(body, opLocalSet, locHotN)
		body = append(body, opEnd)

		// If (((rand >> 16) & 0xFF) % 100) < HotspotTrafficPct -> keyIdx = ((rand >> 24) & 0xFF) % hotN
		// Else -> keyIdx = ((rand >> 24) & 0xFFFF) % keys
		body = append(body, opLocalGet, locRand)
		body = append(body, opI64Const, 16)
		body = append(body, opI64ShrU)
		body = append(body, opI32WrapI64)
		body = append(body, opI32Const)
		body = appendS64(body, 0xFF)
		body = append(body, opI32And)
		body = append(body, opI32Const)
		body = appendS64(body, 100)
		body = append(body, opI32RemU)
		body = append(body, opI32Const)
		body = appendS64(body, int64(spec.HotspotTrafficPct))
		body = append(body, opI32LtU)
		body = append(body, opIf, blockTypeNil)
		body = append(body, opLocalGet, locRand)
		body = append(body, opI64Const, 24)
		body = append(body, opI64ShrU)
		body = append(body, opI32WrapI64)
		body = append(body, opI32Const)
		body = appendS64(body, 0xFF)
		body = append(body, opI32And)
		body = append(body, opLocalGet, locHotN)
		body = append(body, opI32RemU)
		body = append(body, opLocalSet, locKeyIdx)
		body = append(body, opElse)
		body = append(body, opLocalGet, locRand)
		body = append(body, opI64Const, 24)
		body = append(body, opI64ShrU)
		body = append(body, opI32WrapI64)
		body = append(body, opI32Const)
		body = appendS64(body, 0xFFFF)
		body = append(body, opI32And)
		body = append(body, opLocalGet, locKeys)
		body = append(body, opI32RemU)
		body = append(body, opLocalSet, locKeyIdx)
		body = append(body, opEnd)

	default: // DistUniform
		body = append(body, opLocalGet, locRand)
		body = append(body, opI64Const, 16)
		body = append(body, opI64ShrU)
		body = append(body, opI32WrapI64)
		body = append(body, opI32Const)
		body = appendS64(body, 0xFFFF)
		body = append(body, opI32And)
		body = append(body, opLocalGet, locKeys)
		body = append(body, opI32RemU)
		body = append(body, opLocalSet, locKeyIdx)
	}

	// val = ((rand >> 32) & 0xFFFF) % i64.extend_i32_u(values)
	body = append(body, opLocalGet, locRand)
	body = append(body, opI64Const, 32)
	body = append(body, opI64ShrU)
	body = append(body, opI64Const)
	body = appendS64(body, 0xFFFF)
	body = append(body, opI64And)
	body = append(body, opLocalGet, locValues)
	body = append(body, opI64ExtendI32U)
	body = append(body, opI64RemU)
	body = append(body, opLocalSet, locVal)

	// aux = ((rand >> 48) & 0xFFFF) % i64.extend_i32_u(values)
	body = append(body, opLocalGet, locRand)
	body = append(body, opI64Const, 48)
	body = append(body, opI64ShrU)
	body = append(body, opI64Const)
	body = appendS64(body, 0xFFFF)
	body = append(body, opI64And)
	body = append(body, opLocalGet, locValues)
	body = append(body, opI64ExtendI32U)
	body = append(body, opI64RemU)
	body = append(body, opLocalSet, locAux)

	// Return: opCode | (i64(keyIdx) << 8) | (val << 24) | (aux << 44)
	body = append(body, opLocalGet, locOpCode)
	body = append(body, opLocalGet, locKeyIdx)
	body = append(body, opI64ExtendI32U)
	body = append(body, opI64Const, 8)
	body = append(body, opI64Shl)
	body = append(body, opI64Or)
	body = append(body, opLocalGet, locVal)
	body = append(body, opI64Const, 24)
	body = append(body, opI64Shl)
	body = append(body, opI64Or)
	body = append(body, opLocalGet, locAux)
	body = append(body, opI64Const, 44)
	body = append(body, opI64Shl)
	body = append(body, opI64Or)
	body = append(body, opEnd)

	// Local declaration header: 2 groups (3x i32, 3x i64)
	var funcBody []byte
	funcBody = appendU32(funcBody, 2)
	funcBody = appendU32(funcBody, 3)
	funcBody = append(funcBody, valTypeI32)
	funcBody = appendU32(funcBody, 3)
	funcBody = append(funcBody, valTypeI64)
	funcBody = append(funcBody, body...)

	code = appendU32(code, 1) // 1 function body
	code = appendU32(code, uint32(len(funcBody)))
	code = append(code, funcBody...)

	// Assemble full WebAssembly 1.0 module.
	mod := make([]byte, 0, 256)
	mod = append(mod, wasmMagic...)

	// Type section: 1 type `(i32, i32, i64, i32, i32) -> i64`
	typeSec := []byte{
		0x01, // 1 type
		0x60, // func
		0x05, valTypeI32, valTypeI32, valTypeI64, valTypeI32, valTypeI32,
		0x01, valTypeI64,
	}
	mod = appendSection(mod, secType, typeSec)

	// Function section: 1 function of type 0
	funcSec := []byte{0x01, 0x00}
	mod = appendSection(mod, secFunction, funcSec)

	// Export section: export "next_op" -> func 0
	exportName := "next_op"
	var exportSec []byte
	exportSec = appendU32(exportSec, 1)
	exportSec = appendU32(exportSec, uint32(len(exportName)))
	exportSec = append(exportSec, exportName...)
	exportSec = append(exportSec, 0x00, 0x00) // kind=func, index=0
	mod = appendSection(mod, secExport, exportSec)

	// Code section
	mod = appendSection(mod, secCode, code)
	return mod, nil
}

// Plugin is a loaded, validated workload plugin backed by a deterministic
// WebAssembly 1.0 module.
type Plugin struct {
	Name      string
	Source    string // "wasm", "spec", or "preset"
	WasmBytes []byte
	SHA256    string
	Spec      *Spec
	Module    *WasmModule
}

// Load loads a custom workload plugin from a `.wasm` binary path, a `.json`
// declarative spec path, or a built-in preset name (e.g. `preset:cas-hotspot`).
func Load(pathOrPreset string) (*Plugin, error) {
	trimmed := strings.TrimSpace(pathOrPreset)
	if trimmed == "" {
		return nil, fmt.Errorf("workload: empty plugin path or preset name")
	}

	if strings.HasPrefix(strings.ToLower(trimmed), "preset:") {
		spec, ok := Preset(trimmed)
		if !ok {
			return nil, fmt.Errorf("workload: unknown preset %q", trimmed)
		}
		return newPluginFromSpec(spec, "preset")
	}

	data, err := os.ReadFile(trimmed)
	if err != nil {
		// Fall back to preset lookup if the argument matches a built-in preset name.
		if spec, ok := Preset(trimmed); ok {
			return newPluginFromSpec(spec, "preset")
		}
		return nil, fmt.Errorf("workload: reading %s: %w", trimmed, err)
	}

	if IsWasmBinary(data) {
		mod, err := ParseWasm(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", trimmed, err)
		}
		sum := sha256.Sum256(data)
		return &Plugin{
			Name:      strings.TrimSuffix(filepath.Base(trimmed), filepath.Ext(trimmed)),
			Source:    "wasm",
			WasmBytes: data,
			SHA256:    hex.EncodeToString(sum[:]),
			Module:    mod,
		}, nil
	}

	var spec Spec
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return nil, fmt.Errorf("%s: invalid workload spec JSON: %w", trimmed, err)
	}
	if spec.Name == "" {
		spec.Name = strings.TrimSuffix(filepath.Base(trimmed), filepath.Ext(trimmed))
	}
	return newPluginFromSpec(spec, "spec")
}

func newPluginFromSpec(spec Spec, source string) (*Plugin, error) {
	wasmBytes, err := CompileSpecToWasm(spec)
	if err != nil {
		return nil, err
	}
	mod, err := ParseWasm(wasmBytes)
	if err != nil {
		return nil, fmt.Errorf("workload: internal wasm compilation check failed: %w", err)
	}
	sum := sha256.Sum256(wasmBytes)
	specCopy := spec
	return &Plugin{
		Name:      spec.Name,
		Source:    source,
		WasmBytes: wasmBytes,
		SHA256:    hex.EncodeToString(sum[:]),
		Spec:      &specCopy,
		Module:    mod,
	}, nil
}

// NextOp invokes the plugin's Wasm `next_op` export using a single 64-bit draw
// from the process's deterministic PRNG stream and decodes the resulting op.
func (p *Plugin) NextOp(process, step int, keys, values int, r *rng.Rand) (history.Op, error) {
	if keys < 1 {
		keys = 1
	}
	if values < 1 {
		values = 1
	}
	packed, err := p.Module.CallNextOp(int32(process), int32(step), r.Uint64(), int32(keys), int32(values))
	if err != nil {
		return history.Op{}, err
	}
	return UnpackOp(packed, process, keys, values), nil
}
