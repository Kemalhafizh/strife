// Package workload provides deterministic custom workload plugins and a
// zero-dependency WebAssembly 1.0 (MVP) bytecode runner for Strife simulations
// and live cluster chaos tests.
package workload

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// DefaultMaxFuel is the default instruction budget per Wasm next_op invocation.
// It guarantees that a buggy or malicious plugin with an infinite loop cannot
// hang the deterministic simulator or live test runner.
const DefaultMaxFuel = 10_000

// Wasm value type encodings (WebAssembly 1.0 MVP).
const (
	valTypeI32   byte = 0x7f
	valTypeI64   byte = 0x7e
	blockTypeNil byte = 0x40
)

// Wasm section IDs.
const (
	secCustom   byte = 0
	secType     byte = 1
	secFunction byte = 3
	secMemory   byte = 5
	secGlobal   byte = 6
	secExport   byte = 7
	secCode     byte = 10
	secData     byte = 11
)

// Wasm opcodes supported by the deterministic stack machine.
const (
	opUnreachable byte = 0x00
	opNop         byte = 0x01
	opBlock       byte = 0x02
	opLoop        byte = 0x03
	opIf          byte = 0x04
	opElse        byte = 0x05
	opEnd         byte = 0x0b
	opBr          byte = 0x0c
	opBrIf        byte = 0x0d
	opReturn      byte = 0x0f
	opCall        byte = 0x10
	opDrop        byte = 0x1a
	opSelect      byte = 0x1b

	opLocalGet  byte = 0x20
	opLocalSet  byte = 0x21
	opLocalTee  byte = 0x22
	opGlobalGet byte = 0x23
	opGlobalSet byte = 0x24

	opI32Load   byte = 0x28
	opI64Load   byte = 0x29
	opI32Load8U byte = 0x2d
	opI32Store  byte = 0x36
	opI64Store  byte = 0x37
	opI32Store8 byte = 0x3a

	opI32Const byte = 0x41
	opI64Const byte = 0x42

	opI32Eqz byte = 0x45
	opI32Eq  byte = 0x46
	opI32Ne  byte = 0x47
	opI32LtS byte = 0x48
	opI32LtU byte = 0x49
	opI32GtS byte = 0x4a
	opI32GtU byte = 0x4b
	opI32LeS byte = 0x4c
	opI32LeU byte = 0x4d
	opI32GeS byte = 0x4e
	opI32GeU byte = 0x4f

	opI64Eqz byte = 0x50
	opI64Eq  byte = 0x51
	opI64Ne  byte = 0x52
	opI64LtS byte = 0x53
	opI64LtU byte = 0x54
	opI64GtS byte = 0x55
	opI64GtU byte = 0x56
	opI64LeS byte = 0x57
	opI64LeU byte = 0x58
	opI64GeS byte = 0x59
	opI64GeU byte = 0x5a

	opI32Add  byte = 0x6a
	opI32Sub  byte = 0x6b
	opI32Mul  byte = 0x6c
	opI32DivS byte = 0x6d
	opI32DivU byte = 0x6e
	opI32RemS byte = 0x6f
	opI32RemU byte = 0x70
	opI32And  byte = 0x71
	opI32Or   byte = 0x72
	opI32Xor  byte = 0x73
	opI32Shl  byte = 0x74
	opI32ShrS byte = 0x75
	opI32ShrU byte = 0x76

	opI64Add  byte = 0x7c
	opI64Sub  byte = 0x7d
	opI64Mul  byte = 0x7e
	opI64DivS byte = 0x7f
	opI64DivU byte = 0x80
	opI64RemS byte = 0x81
	opI64RemU byte = 0x82
	opI64And  byte = 0x83
	opI64Or   byte = 0x84
	opI64Xor  byte = 0x85
	opI64Shl  byte = 0x86
	opI64ShrS byte = 0x87
	opI64ShrU byte = 0x88

	opI32WrapI64    byte = 0xa7
	opI64ExtendI32S byte = 0xac
	opI64ExtendI32U byte = 0xad
)

var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

// IsWasmBinary reports whether data starts with the 8-byte WebAssembly 1.0 header.
func IsWasmBinary(data []byte) bool {
	return len(data) >= 8 && bytes.Equal(data[:8], wasmMagic)
}

type funcType struct {
	params  []byte
	results []byte
}

type wasmInstr struct {
	op       byte
	immI64   int64
	immU32   uint32
	blockRes int // 0 or 1 result value on block exit
	elseIdx  int // for opIf: index of matching opElse (-1 if none)
	endIdx   int // for opBlock/opLoop/opIf/opElse: index of matching opEnd
}

type wasmFunc struct {
	typeIdx uint32
	locals  []byte
	code    []wasmInstr
}

type wasmGlobal struct {
	valType byte
	mutable bool
	initVal uint64
}

// WasmModule is a parsed, validated WebAssembly 1.0 module ready for
// concurrent, fuel-metered deterministic execution.
type WasmModule struct {
	types       []funcType
	funcs       []wasmFunc
	globals     []wasmGlobal
	exports     map[string]uint32
	initialMem  []byte
	nextOpIndex int
	maxFuel     int
}

// ParseWasm decodes and validates a WebAssembly 1.0 binary module.
// The module must export a function `next_op(i32, i32, i64, i32, i32) -> i64`.
func ParseWasm(data []byte) (*WasmModule, error) {
	if !IsWasmBinary(data) {
		return nil, fmt.Errorf("wasm: invalid magic header or version (expected \\x00asm v1)")
	}
	r := &wasmReader{buf: data, pos: 8}
	mod := &WasmModule{
		exports:     make(map[string]uint32),
		nextOpIndex: -1,
		maxFuel:     DefaultMaxFuel,
	}

	var funcTypeIndices []uint32
	var memPages uint32

	for r.pos < len(r.buf) {
		secID, err := r.readByte()
		if err != nil {
			return nil, fmt.Errorf("wasm: reading section id: %w", err)
		}
		secLen, err := r.readU32()
		if err != nil {
			return nil, fmt.Errorf("wasm: reading section %d length: %w", secID, err)
		}
		if r.pos+int(secLen) > len(r.buf) {
			return nil, fmt.Errorf("wasm: section %d truncated (need %d bytes)", secID, secLen)
		}
		secEnd := r.pos + int(secLen)

		switch secID {
		case secCustom:
			r.pos = secEnd

		case secType:
			count, err := r.readU32()
			if err != nil {
				return nil, err
			}
			for i := uint32(0); i < count; i++ {
				tag, err := r.readByte()
				if err != nil || tag != 0x60 {
					return nil, fmt.Errorf("wasm: invalid functype tag 0x%02x", tag)
				}
				params, err := r.readValTypeVec()
				if err != nil {
					return nil, err
				}
				results, err := r.readValTypeVec()
				if err != nil {
					return nil, err
				}
				mod.types = append(mod.types, funcType{params: params, results: results})
			}

		case secFunction:
			count, err := r.readU32()
			if err != nil {
				return nil, err
			}
			for i := uint32(0); i < count; i++ {
				tidx, err := r.readU32()
				if err != nil {
					return nil, err
				}
				if int(tidx) >= len(mod.types) {
					return nil, fmt.Errorf("wasm: function %d references unknown type index %d", i, tidx)
				}
				funcTypeIndices = append(funcTypeIndices, tidx)
			}

		case secMemory:
			count, err := r.readU32()
			if err != nil {
				return nil, err
			}
			for i := uint32(0); i < count; i++ {
				flags, err := r.readByte()
				if err != nil {
					return nil, err
				}
				minPages, err := r.readU32()
				if err != nil {
					return nil, err
				}
				if flags&0x01 != 0 {
					if _, err := r.readU32(); err != nil {
						return nil, err
					}
				}
				if minPages > 16 {
					return nil, fmt.Errorf("wasm: memory minimum %d pages exceeds 16-page (1 MiB) sandbox cap", minPages)
				}
				memPages = minPages
			}

		case secGlobal:
			count, err := r.readU32()
			if err != nil {
				return nil, err
			}
			for i := uint32(0); i < count; i++ {
				vt, err := r.readByte()
				if err != nil {
					return nil, err
				}
				mut, err := r.readByte()
				if err != nil {
					return nil, err
				}
				initVal, err := r.readConstExpr()
				if err != nil {
					return nil, fmt.Errorf("wasm: global %d init: %w", i, err)
				}
				mod.globals = append(mod.globals, wasmGlobal{
					valType: vt,
					mutable: mut != 0,
					initVal: initVal,
				})
			}

		case secExport:
			count, err := r.readU32()
			if err != nil {
				return nil, err
			}
			for i := uint32(0); i < count; i++ {
				name, err := r.readString()
				if err != nil {
					return nil, err
				}
				kind, err := r.readByte()
				if err != nil {
					return nil, err
				}
				idx, err := r.readU32()
				if err != nil {
					return nil, err
				}
				if kind == 0x00 { // function export
					mod.exports[name] = idx
				}
			}

		case secCode:
			count, err := r.readU32()
			if err != nil {
				return nil, err
			}
			if int(count) != len(funcTypeIndices) {
				return nil, fmt.Errorf("wasm: code section count (%d) does not match function section count (%d)", count, len(funcTypeIndices))
			}
			for i := uint32(0); i < count; i++ {
				bodySize, err := r.readU32()
				if err != nil {
					return nil, err
				}
				bodyEnd := r.pos + int(bodySize)
				if bodyEnd > secEnd {
					return nil, fmt.Errorf("wasm: function %d body exceeds code section", i)
				}
				fn, err := parseFuncBody(r, bodyEnd, funcTypeIndices[i])
				if err != nil {
					return nil, fmt.Errorf("wasm: function %d: %w", i, err)
				}
				mod.funcs = append(mod.funcs, fn)
				r.pos = bodyEnd
			}

		case secData:
			count, err := r.readU32()
			if err != nil {
				return nil, err
			}
			if memPages == 0 {
				memPages = 1
			}
			if len(mod.initialMem) == 0 {
				mod.initialMem = make([]byte, memPages*65536)
			}
			for i := uint32(0); i < count; i++ {
				mode, err := r.readU32()
				if err != nil {
					return nil, err
				}
				if mode != 0 {
					return nil, fmt.Errorf("wasm: unsupported data segment mode %d", mode)
				}
				offset, err := r.readConstExpr()
				if err != nil {
					return nil, err
				}
				size, err := r.readU32()
				if err != nil {
					return nil, err
				}
				if r.pos+int(size) > secEnd {
					return nil, fmt.Errorf("wasm: data segment %d truncated", i)
				}
				if int(offset)+int(size) > len(mod.initialMem) {
					return nil, fmt.Errorf("wasm: data segment %d out of memory bounds", i)
				}
				copy(mod.initialMem[offset:], r.buf[r.pos:r.pos+int(size)])
				r.pos += int(size)
			}

		default:
			r.pos = secEnd
		}

		if r.pos != secEnd {
			return nil, fmt.Errorf("wasm: section %d size mismatch (pos=%d, expected=%d)", secID, r.pos, secEnd)
		}
	}

	if memPages > 0 && len(mod.initialMem) == 0 {
		mod.initialMem = make([]byte, memPages*65536)
	}

	idx, ok := mod.exports["next_op"]
	if !ok {
		return nil, fmt.Errorf("wasm: module must export function \"next_op\"")
	}
	if int(idx) >= len(mod.funcs) {
		return nil, fmt.Errorf("wasm: exported \"next_op\" references invalid function index %d", idx)
	}
	ft := mod.types[mod.funcs[idx].typeIdx]
	wantParams := []byte{valTypeI32, valTypeI32, valTypeI64, valTypeI32, valTypeI32}
	wantResults := []byte{valTypeI64}
	if !bytes.Equal(ft.params, wantParams) || !bytes.Equal(ft.results, wantResults) {
		return nil, fmt.Errorf("wasm: \"next_op\" must have signature (i32, i32, i64, i32, i32) -> i64")
	}
	mod.nextOpIndex = int(idx)
	return mod, nil
}

func parseFuncBody(r *wasmReader, bodyEnd int, typeIdx uint32) (wasmFunc, error) {
	localGroupCount, err := r.readU32()
	if err != nil {
		return wasmFunc{}, err
	}
	var locals []byte
	for i := uint32(0); i < localGroupCount; i++ {
		n, err := r.readU32()
		if err != nil {
			return wasmFunc{}, err
		}
		vt, err := r.readByte()
		if err != nil {
			return wasmFunc{}, err
		}
		if vt != valTypeI32 && vt != valTypeI64 {
			return wasmFunc{}, fmt.Errorf("unsupported local value type 0x%02x", vt)
		}
		if len(locals)+int(n) > 256 {
			return wasmFunc{}, fmt.Errorf("too many locals in function (max 256)")
		}
		for j := uint32(0); j < n; j++ {
			locals = append(locals, vt)
		}
	}

	var code []wasmInstr
	type ctrlOpen struct {
		instrIdx int
		elseIdx  int
	}
	// Implicit function-level block at index 0 so `br` to top-level works uniformly.
	code = append(code, wasmInstr{op: opBlock, blockRes: 1, elseIdx: -1, endIdx: -1})
	stack := []ctrlOpen{{instrIdx: 0, elseIdx: -1}}

	for r.pos < bodyEnd {
		op, err := r.readByte()
		if err != nil {
			return wasmFunc{}, err
		}
		ins := wasmInstr{op: op, elseIdx: -1, endIdx: -1}
		switch op {
		case opUnreachable, opNop, opReturn, opDrop, opSelect,
			opI32Eqz, opI32Eq, opI32Ne, opI32LtS, opI32LtU, opI32GtS, opI32GtU, opI32LeS, opI32LeU, opI32GeS, opI32GeU,
			opI64Eqz, opI64Eq, opI64Ne, opI64LtS, opI64LtU, opI64GtS, opI64GtU, opI64LeS, opI64LeU, opI64GeS, opI64GeU,
			opI32Add, opI32Sub, opI32Mul, opI32DivS, opI32DivU, opI32RemS, opI32RemU, opI32And, opI32Or, opI32Xor, opI32Shl, opI32ShrS, opI32ShrU,
			opI64Add, opI64Sub, opI64Mul, opI64DivS, opI64DivU, opI64RemS, opI64RemU, opI64And, opI64Or, opI64Xor, opI64Shl, opI64ShrS, opI64ShrU,
			opI32WrapI64, opI64ExtendI32S, opI64ExtendI32U:
			// No immediate operands.

		case opBlock, opLoop, opIf:
			bt, err := r.readByte()
			if err != nil {
				return wasmFunc{}, err
			}
			switch bt {
			case blockTypeNil:
				ins.blockRes = 0
			case valTypeI32, valTypeI64:
				ins.blockRes = 1
			default:
				return wasmFunc{}, fmt.Errorf("unsupported block type 0x%02x", bt)
			}
			idx := len(code)
			code = append(code, ins)
			stack = append(stack, ctrlOpen{instrIdx: idx, elseIdx: -1})
			continue

		case opElse:
			if len(stack) <= 1 {
				return wasmFunc{}, fmt.Errorf("unexpected else opcode")
			}
			top := &stack[len(stack)-1]
			if code[top.instrIdx].op != opIf || top.elseIdx != -1 {
				return wasmFunc{}, fmt.Errorf("else without matching if")
			}
			idx := len(code)
			code[top.instrIdx].elseIdx = idx
			top.elseIdx = idx

		case opEnd:
			if len(stack) == 0 {
				return wasmFunc{}, fmt.Errorf("unbalanced end opcode")
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			idx := len(code)
			code[top.instrIdx].endIdx = idx
			if top.elseIdx != -1 {
				code[top.elseIdx].endIdx = idx
			}

		case opBr, opBrIf, opCall, opLocalGet, opLocalSet, opLocalTee, opGlobalGet, opGlobalSet:
			v, err := r.readU32()
			if err != nil {
				return wasmFunc{}, err
			}
			ins.immU32 = v

		case opI32Load, opI64Load, opI32Load8U, opI32Store, opI64Store, opI32Store8:
			if _, err := r.readU32(); err != nil { // align
				return wasmFunc{}, err
			}
			offset, err := r.readU32()
			if err != nil {
				return wasmFunc{}, err
			}
			ins.immU32 = offset

		case opI32Const:
			v, err := r.readS32()
			if err != nil {
				return wasmFunc{}, err
			}
			ins.immI64 = int64(v)

		case opI64Const:
			v, err := r.readS64()
			if err != nil {
				return wasmFunc{}, err
			}
			ins.immI64 = v

		default:
			return wasmFunc{}, fmt.Errorf("unsupported wasm opcode 0x%02x", op)
		}
		code = append(code, ins)
	}

	if len(stack) != 0 {
		return wasmFunc{}, fmt.Errorf("unclosed control block at end of function")
	}
	return wasmFunc{
		typeIdx: typeIdx,
		locals:  locals,
		code:    code,
	}, nil
}

// SetMaxFuel overrides the per-call instruction fuel limit.
func (m *WasmModule) SetMaxFuel(fuel int) {
	if fuel > 0 {
		m.maxFuel = fuel
	}
}

type ctrlFrame struct {
	op       byte
	startPC  int
	endPC    int
	stackLen int
	resCount int
}

// CallNextOp invokes the exported `next_op(process, step, rand_u64, keys, values) -> i64`
// function in an isolated, fuel-metered stack frame. It is safe for concurrent use.
func (m *WasmModule) CallNextOp(process, step int32, randU64 uint64, keys, values int32) (uint64, error) {
	if m.nextOpIndex < 0 || m.nextOpIndex >= len(m.funcs) {
		return 0, fmt.Errorf("wasm: next_op not bound")
	}
	fuel := m.maxFuel
	if fuel <= 0 {
		fuel = DefaultMaxFuel
	}

	var mem []byte
	if len(m.initialMem) > 0 {
		mem = make([]byte, len(m.initialMem))
		copy(mem, m.initialMem)
	}
	var globals []uint64
	if len(m.globals) > 0 {
		globals = make([]uint64, len(m.globals))
		for i, g := range m.globals {
			globals[i] = g.initVal
		}
	}

	args := []uint64{
		uint64(uint32(process)),
		uint64(uint32(step)),
		randU64,
		uint64(uint32(keys)),
		uint64(uint32(values)),
	}
	return m.execFunc(m.nextOpIndex, args, &fuel, mem, globals, 0)
}

func (m *WasmModule) execFunc(funcIdx int, args []uint64, fuel *int, mem []byte, globals []uint64, depth int) (uint64, error) {
	if depth > 32 {
		return 0, fmt.Errorf("wasm: call stack depth limit (32) exceeded")
	}
	fn := m.funcs[funcIdx]
	ft := m.types[fn.typeIdx]
	locals := make([]uint64, len(ft.params)+len(fn.locals))
	copy(locals, args)

	stack := make([]uint64, 0, 32)
	ctrls := make([]ctrlFrame, 0, 16)

	pop := func() uint64 {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return v
	}
	push := func(v uint64) {
		stack = append(stack, v)
	}

	code := fn.code
	pc := 0
	for pc < len(code) {
		*fuel--
		if *fuel < 0 {
			return 0, fmt.Errorf("wasm: instruction fuel exhausted (limit %d)", m.maxFuel)
		}
		ins := code[pc]
		switch ins.op {
		case opUnreachable:
			return 0, fmt.Errorf("wasm: unreachable executed")

		case opNop:
			pc++

		case opBlock, opLoop:
			resCount := ins.blockRes
			if pc == 0 {
				resCount = len(ft.results)
			}
			ctrls = append(ctrls, ctrlFrame{
				op:       ins.op,
				startPC:  pc,
				endPC:    ins.endIdx,
				stackLen: len(stack),
				resCount: resCount,
			})
			pc++

		case opIf:
			cond := uint32(pop())
			ctrls = append(ctrls, ctrlFrame{
				op:       opIf,
				startPC:  pc,
				endPC:    ins.endIdx,
				stackLen: len(stack),
				resCount: ins.blockRes,
			})
			if cond != 0 {
				pc++
			} else if ins.elseIdx != -1 {
				pc = ins.elseIdx + 1
			} else {
				ctrls = ctrls[:len(ctrls)-1]
				pc = ins.endIdx + 1
			}

		case opElse:
			// Reached the end of the true branch of an `if`: jump past `end`.
			if len(ctrls) == 0 {
				return 0, fmt.Errorf("wasm: control stack underflow on else")
			}
			ctrls = ctrls[:len(ctrls)-1]
			pc = ins.endIdx + 1

		case opEnd:
			if len(ctrls) > 0 {
				ctrls = ctrls[:len(ctrls)-1]
			}
			pc++

		case opBr, opBrIf:
			if ins.op == opBrIf {
				cond := uint32(pop())
				if cond == 0 {
					pc++
					continue
				}
			}
			label := int(ins.immU32)
			if label >= len(ctrls) {
				return 0, fmt.Errorf("wasm: branch label %d out of range", label)
			}
			targetIdx := len(ctrls) - 1 - label
			target := ctrls[targetIdx]
			if target.op == opLoop {
				stack = stack[:target.stackLen]
				ctrls = ctrls[:targetIdx+1]
				pc = target.startPC + 1
			} else {
				var res uint64
				if target.resCount > 0 && len(stack) > 0 {
					res = pop()
				}
				stack = stack[:target.stackLen]
				if target.resCount > 0 {
					push(res)
				}
				ctrls = ctrls[:targetIdx]
				pc = target.endPC + 1
			}

		case opReturn:
			if len(ft.results) > 0 {
				return pop(), nil
			}
			return 0, nil

		case opCall:
			calleeIdx := int(ins.immU32)
			if calleeIdx < 0 || calleeIdx >= len(m.funcs) {
				return 0, fmt.Errorf("wasm: call to invalid function %d", calleeIdx)
			}
			calleeType := m.types[m.funcs[calleeIdx].typeIdx]
			calleeArgs := make([]uint64, len(calleeType.params))
			for i := len(calleeType.params) - 1; i >= 0; i-- {
				calleeArgs[i] = pop()
			}
			ret, err := m.execFunc(calleeIdx, calleeArgs, fuel, mem, globals, depth+1)
			if err != nil {
				return 0, err
			}
			if len(calleeType.results) > 0 {
				push(ret)
			}
			pc++

		case opDrop:
			_ = pop()
			pc++

		case opSelect:
			c := uint32(pop())
			v2 := pop()
			v1 := pop()
			if c != 0 {
				push(v1)
			} else {
				push(v2)
			}
			pc++

		case opLocalGet:
			idx := int(ins.immU32)
			if idx >= len(locals) {
				return 0, fmt.Errorf("wasm: local.get index %d out of range", idx)
			}
			push(locals[idx])
			pc++

		case opLocalSet:
			idx := int(ins.immU32)
			if idx >= len(locals) {
				return 0, fmt.Errorf("wasm: local.set index %d out of range", idx)
			}
			locals[idx] = pop()
			pc++

		case opLocalTee:
			idx := int(ins.immU32)
			if idx >= len(locals) {
				return 0, fmt.Errorf("wasm: local.tee index %d out of range", idx)
			}
			locals[idx] = stack[len(stack)-1]
			pc++

		case opGlobalGet:
			idx := int(ins.immU32)
			if idx >= len(globals) {
				return 0, fmt.Errorf("wasm: global.get index %d out of range", idx)
			}
			push(globals[idx])
			pc++

		case opGlobalSet:
			idx := int(ins.immU32)
			if idx >= len(globals) {
				return 0, fmt.Errorf("wasm: global.set index %d out of range", idx)
			}
			globals[idx] = pop()
			pc++

		case opI32Load:
			addr := uint64(uint32(pop())) + uint64(ins.immU32)
			if addr+4 > uint64(len(mem)) {
				return 0, fmt.Errorf("wasm: out of bounds i32.load at %d", addr)
			}
			push(uint64(binary.LittleEndian.Uint32(mem[addr : addr+4])))
			pc++

		case opI64Load:
			addr := uint64(uint32(pop())) + uint64(ins.immU32)
			if addr+8 > uint64(len(mem)) {
				return 0, fmt.Errorf("wasm: out of bounds i64.load at %d", addr)
			}
			push(binary.LittleEndian.Uint64(mem[addr : addr+8]))
			pc++

		case opI32Load8U:
			addr := uint64(uint32(pop())) + uint64(ins.immU32)
			if addr+1 > uint64(len(mem)) {
				return 0, fmt.Errorf("wasm: out of bounds i32.load8_u at %d", addr)
			}
			push(uint64(mem[addr]))
			pc++

		case opI32Store:
			val := uint32(pop())
			addr := uint64(uint32(pop())) + uint64(ins.immU32)
			if addr+4 > uint64(len(mem)) {
				return 0, fmt.Errorf("wasm: out of bounds i32.store at %d", addr)
			}
			binary.LittleEndian.PutUint32(mem[addr:addr+4], val)
			pc++

		case opI64Store:
			val := pop()
			addr := uint64(uint32(pop())) + uint64(ins.immU32)
			if addr+8 > uint64(len(mem)) {
				return 0, fmt.Errorf("wasm: out of bounds i64.store at %d", addr)
			}
			binary.LittleEndian.PutUint64(mem[addr:addr+8], val)
			pc++

		case opI32Store8:
			val := byte(pop())
			addr := uint64(uint32(pop())) + uint64(ins.immU32)
			if addr+1 > uint64(len(mem)) {
				return 0, fmt.Errorf("wasm: out of bounds i32.store8 at %d", addr)
			}
			mem[addr] = val
			pc++

		case opI32Const:
			push(uint64(uint32(int32(ins.immI64))))
			pc++

		case opI64Const:
			push(uint64(ins.immI64))
			pc++

		case opI32Eqz:
			if uint32(pop()) == 0 {
				push(1)
			} else {
				push(0)
			}
			pc++

		case opI32Eq, opI32Ne, opI32LtS, opI32LtU, opI32GtS, opI32GtU, opI32LeS, opI32LeU, opI32GeS, opI32GeU:
			b := uint32(pop())
			a := uint32(pop())
			var res bool
			switch ins.op {
			case opI32Eq:
				res = a == b
			case opI32Ne:
				res = a != b
			case opI32LtS:
				res = int32(a) < int32(b)
			case opI32LtU:
				res = a < b
			case opI32GtS:
				res = int32(a) > int32(b)
			case opI32GtU:
				res = a > b
			case opI32LeS:
				res = int32(a) <= int32(b)
			case opI32LeU:
				res = a <= b
			case opI32GeS:
				res = int32(a) >= int32(b)
			case opI32GeU:
				res = a >= b
			}
			if res {
				push(1)
			} else {
				push(0)
			}
			pc++

		case opI64Eqz:
			if pop() == 0 {
				push(1)
			} else {
				push(0)
			}
			pc++

		case opI64Eq, opI64Ne, opI64LtS, opI64LtU, opI64GtS, opI64GtU, opI64LeS, opI64LeU, opI64GeS, opI64GeU:
			b := pop()
			a := pop()
			var res bool
			switch ins.op {
			case opI64Eq:
				res = a == b
			case opI64Ne:
				res = a != b
			case opI64LtS:
				res = int64(a) < int64(b)
			case opI64LtU:
				res = a < b
			case opI64GtS:
				res = int64(a) > int64(b)
			case opI64GtU:
				res = a > b
			case opI64LeS:
				res = int64(a) <= int64(b)
			case opI64LeU:
				res = a <= b
			case opI64GeS:
				res = int64(a) >= int64(b)
			case opI64GeU:
				res = a >= b
			}
			if res {
				push(1)
			} else {
				push(0)
			}
			pc++

		case opI32Add, opI32Sub, opI32Mul, opI32DivS, opI32DivU, opI32RemS, opI32RemU, opI32And, opI32Or, opI32Xor, opI32Shl, opI32ShrS, opI32ShrU:
			b := uint32(pop())
			a := uint32(pop())
			var out uint32
			switch ins.op {
			case opI32Add:
				out = a + b
			case opI32Sub:
				out = a - b
			case opI32Mul:
				out = a * b
			case opI32DivS:
				if b == 0 {
					return 0, fmt.Errorf("wasm: integer divide by zero")
				}
				if int32(a) == -2147483648 && int32(b) == -1 {
					return 0, fmt.Errorf("wasm: integer overflow in i32.div_s")
				}
				out = uint32(int32(a) / int32(b))
			case opI32DivU:
				if b == 0 {
					return 0, fmt.Errorf("wasm: integer divide by zero")
				}
				out = a / b
			case opI32RemS:
				if b == 0 {
					return 0, fmt.Errorf("wasm: integer divide by zero")
				}
				if int32(a) == -2147483648 && int32(b) == -1 {
					out = 0
				} else {
					out = uint32(int32(a) % int32(b))
				}
			case opI32RemU:
				if b == 0 {
					return 0, fmt.Errorf("wasm: integer divide by zero")
				}
				out = a % b
			case opI32And:
				out = a & b
			case opI32Or:
				out = a | b
			case opI32Xor:
				out = a ^ b
			case opI32Shl:
				out = a << (b & 31)
			case opI32ShrS:
				out = uint32(int32(a) >> (b & 31))
			case opI32ShrU:
				out = a >> (b & 31)
			}
			push(uint64(out))
			pc++

		case opI64Add, opI64Sub, opI64Mul, opI64DivS, opI64DivU, opI64RemS, opI64RemU, opI64And, opI64Or, opI64Xor, opI64Shl, opI64ShrS, opI64ShrU:
			b := pop()
			a := pop()
			var out uint64
			switch ins.op {
			case opI64Add:
				out = a + b
			case opI64Sub:
				out = a - b
			case opI64Mul:
				out = a * b
			case opI64DivS:
				if b == 0 {
					return 0, fmt.Errorf("wasm: integer divide by zero")
				}
				if int64(a) == -9223372036854775808 && int64(b) == -1 {
					return 0, fmt.Errorf("wasm: integer overflow in i64.div_s")
				}
				out = uint64(int64(a) / int64(b))
			case opI64DivU:
				if b == 0 {
					return 0, fmt.Errorf("wasm: integer divide by zero")
				}
				out = a / b
			case opI64RemS:
				if b == 0 {
					return 0, fmt.Errorf("wasm: integer divide by zero")
				}
				if int64(a) == -9223372036854775808 && int64(b) == -1 {
					out = 0
				} else {
					out = uint64(int64(a) % int64(b))
				}
			case opI64RemU:
				if b == 0 {
					return 0, fmt.Errorf("wasm: integer divide by zero")
				}
				out = a % b
			case opI64And:
				out = a & b
			case opI64Or:
				out = a | b
			case opI64Xor:
				out = a ^ b
			case opI64Shl:
				out = a << (b & 63)
			case opI64ShrS:
				out = uint64(int64(a) >> (b & 63))
			case opI64ShrU:
				out = a >> (b & 63)
			}
			push(out)
			pc++

		case opI32WrapI64:
			push(uint64(uint32(pop())))
			pc++

		case opI64ExtendI32S:
			push(uint64(int64(int32(uint32(pop())))))
			pc++

		case opI64ExtendI32U:
			push(uint64(uint32(pop())))
			pc++
		}
	}

	if len(ft.results) > 0 {
		if len(stack) == 0 {
			return 0, fmt.Errorf("wasm: empty stack at function return")
		}
		return stack[len(stack)-1], nil
	}
	return 0, nil
}

type wasmReader struct {
	buf []byte
	pos int
}

func (r *wasmReader) readByte() (byte, error) {
	if r.pos >= len(r.buf) {
		return 0, fmt.Errorf("unexpected EOF")
	}
	b := r.buf[r.pos]
	r.pos++
	return b, nil
}

func (r *wasmReader) readU32() (uint32, error) {
	var res uint32
	var shift uint
	for i := 0; i < 5; i++ {
		b, err := r.readByte()
		if err != nil {
			return 0, err
		}
		res |= uint32(b&0x7f) << shift
		if b&0x80 == 0 {
			return res, nil
		}
		shift += 7
	}
	return 0, fmt.Errorf("uleb128 overflow")
}

func (r *wasmReader) readS32() (int32, error) {
	var res int32
	var shift uint
	var b byte
	var err error
	for i := 0; i < 5; i++ {
		b, err = r.readByte()
		if err != nil {
			return 0, err
		}
		res |= int32(b&0x7f) << shift
		shift += 7
		if b&0x80 == 0 {
			if shift < 32 && (b&0x40) != 0 {
				res |= ^int32(0) << shift
			}
			return res, nil
		}
	}
	return 0, fmt.Errorf("sleb128_32 overflow")
}

func (r *wasmReader) readS64() (int64, error) {
	var res int64
	var shift uint
	var b byte
	var err error
	for i := 0; i < 10; i++ {
		b, err = r.readByte()
		if err != nil {
			return 0, err
		}
		res |= int64(b&0x7f) << shift
		shift += 7
		if b&0x80 == 0 {
			if shift < 64 && (b&0x40) != 0 {
				res |= ^int64(0) << shift
			}
			return res, nil
		}
	}
	return 0, fmt.Errorf("sleb128_64 overflow")
}

func (r *wasmReader) readValTypeVec() ([]byte, error) {
	n, err := r.readU32()
	if err != nil {
		return nil, err
	}
	if r.pos+int(n) > len(r.buf) {
		return nil, fmt.Errorf("unexpected EOF reading type vector")
	}
	out := make([]byte, n)
	for i := uint32(0); i < n; i++ {
		vt, err := r.readByte()
		if err != nil {
			return nil, err
		}
		if vt != valTypeI32 && vt != valTypeI64 {
			return nil, fmt.Errorf("unsupported value type 0x%02x", vt)
		}
		out[i] = vt
	}
	return out, nil
}

func (r *wasmReader) readString() (string, error) {
	n, err := r.readU32()
	if err != nil {
		return "", err
	}
	if r.pos+int(n) > len(r.buf) {
		return "", fmt.Errorf("unexpected EOF reading string")
	}
	s := string(r.buf[r.pos : r.pos+int(n)])
	r.pos += int(n)
	return s, nil
}

func (r *wasmReader) readConstExpr() (uint64, error) {
	op, err := r.readByte()
	if err != nil {
		return 0, err
	}
	var val uint64
	switch op {
	case opI32Const:
		v, err := r.readS32()
		if err != nil {
			return 0, err
		}
		val = uint64(uint32(v))
	case opI64Const:
		v, err := r.readS64()
		if err != nil {
			return 0, err
		}
		val = uint64(v)
	default:
		return 0, fmt.Errorf("unsupported const expr opcode 0x%02x", op)
	}
	end, err := r.readByte()
	if err != nil || end != opEnd {
		return 0, fmt.Errorf("const expr missing end opcode")
	}
	return val, nil
}

func appendU32(buf []byte, v uint32) []byte {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		buf = append(buf, b)
		if v == 0 {
			break
		}
	}
	return buf
}

func appendS64(buf []byte, v int64) []byte {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		done := (v == 0 && (b&0x40) == 0) || (v == -1 && (b&0x40) != 0)
		if !done {
			b |= 0x80
		}
		buf = append(buf, b)
		if done {
			break
		}
	}
	return buf
}

func appendSection(mod []byte, id byte, payload []byte) []byte {
	mod = append(mod, id)
	mod = appendU32(mod, uint32(len(payload)))
	mod = append(mod, payload...)
	return mod
}
