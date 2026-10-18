//go:build js && wasm

// Command wasm exposes the bridge to JavaScript as globalThis.strife. Every
// function takes and returns JSON strings (bytes travel as Uint8Array) and
// answers {"ok":true,"result":…} or {"ok":false,"error":"…"}, so a bad input
// or a panic never takes the Go runtime down with it.
package main

import (
	"encoding/json"
	"fmt"
	"syscall/js"

	"github.com/kemalhafizh/strife/pkg/report"
	"github.com/kemalhafizh/strife/site/wasm/bridge"
)

func main() {
	api := js.Global().Get("Object").New()
	api.Set("version", report.Version)
	api.Set("systems", fn(func(_ []js.Value) (any, error) { return bridge.Systems(), nil }))
	api.Set("faultKinds", fn(func(_ []js.Value) (any, error) { return bridge.FaultKinds(), nil }))
	api.Set("models", fn(func(_ []js.Value) (any, error) { return bridge.Models(), nil }))
	api.Set("simulate", fn(func(args []js.Value) (any, error) {
		var req bridge.SimRequest
		if err := decode(args, 0, &req); err != nil {
			return nil, err
		}
		return bridge.Simulate(req)
	}))
	api.Set("check", fn(func(args []js.Value) (any, error) {
		var req bridge.CheckRequest
		if err := decode(args, 1, &req); err != nil {
			return nil, err
		}
		return bridge.Check(bytesArg(args, 0), req)
	}))
	api.Set("verifyAttestation", fn(func(args []js.Value) (any, error) {
		return bridge.VerifyAttestation(bytesArg(args, 0), bytesArg(args, 1), bytesArg(args, 2))
	}))
	api.Set("verifyRelease", fn(func(args []js.Value) (any, error) {
		var files []bridge.NamedFile
		if len(args) > 2 && args[2].Truthy() {
			list := args[2]
			for i := 0; i < list.Length(); i++ {
				f := list.Index(i)
				files = append(files, bridge.NamedFile{Name: f.Get("name").String(), Data: toBytes(f.Get("data"))})
			}
		}
		return bridge.VerifyRelease(bytesArg(args, 0), bytesArg(args, 1), files)
	}))
	js.Global().Set("strife", api)
	if ready := js.Global().Get("onStrifeReady"); ready.Type() == js.TypeFunction {
		ready.Invoke()
	}
	select {}
}

// fn wraps a call so it always answers a JSON envelope.
func fn(call func(args []js.Value) (any, error)) js.Func {
	return js.FuncOf(func(_ js.Value, args []js.Value) (out any) {
		defer func() {
			if p := recover(); p != nil {
				out = envelope(nil, fmt.Errorf("internal error: %v", p))
			}
		}()
		return envelope(call(args))
	})
}

func envelope(result any, err error) string {
	var v any = map[string]any{"ok": true, "result": result}
	if err != nil {
		v = map[string]any{"ok": false, "error": err.Error()}
	}
	b, merr := json.Marshal(v)
	if merr != nil {
		b, _ = json.Marshal(map[string]any{"ok": false, "error": merr.Error()})
	}
	return string(b)
}

func decode(args []js.Value, i int, v any) error {
	if len(args) <= i || args[i].Type() != js.TypeString {
		return fmt.Errorf("argument %d: expected a JSON string", i+1)
	}
	return json.Unmarshal([]byte(args[i].String()), v)
}

func bytesArg(args []js.Value, i int) []byte {
	if len(args) <= i || !args[i].Truthy() {
		return nil
	}
	return toBytes(args[i])
}

func toBytes(v js.Value) []byte {
	if v.Type() == js.TypeString {
		return []byte(v.String())
	}
	b := make([]byte, v.Get("length").Int())
	js.CopyBytesToGo(b, v)
	return b
}
