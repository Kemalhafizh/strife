// Package jsonschema derives JSON Schemas (draft 2020-12 subset) from Go
// types, validates JSON documents against them, and checks that a new
// version of a schema is backward compatible with an old one. Strife's
// output formats are versioned contracts: enterprises archive reports for
// years, so a field may be added but never renamed, retyped or removed.
package jsonschema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Schema is a JSON Schema document.
type Schema = map[string]any

var (
	durationType = reflect.TypeOf(time.Duration(0))
	timeType     = reflect.TypeOf(time.Time{})
	rawType      = reflect.TypeOf(json.RawMessage(nil))
)

// Generate returns the schema of values of type t, with every named struct
// type in $defs.
func Generate(t reflect.Type, id, title string) Schema {
	g := &gen{defs: map[string]any{}}
	root := g.of(t)
	root["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	root["$id"] = id
	root["title"] = title
	if len(g.defs) > 0 {
		root["$defs"] = g.defs
	}
	return root
}

type gen struct{ defs map[string]any }

func defName(t reflect.Type) string {
	return strings.ReplaceAll(t.PkgPath()[strings.LastIndex(t.PkgPath(), "/")+1:]+"."+t.Name(), "/", ".")
}

func (g *gen) of(t reflect.Type) Schema {
	switch t {
	case durationType:
		return Schema{"type": "integer", "description": "nanoseconds"}
	case timeType:
		return Schema{"type": "string", "format": "date-time"}
	case rawType:
		return Schema{}
	}
	switch t.Kind() {
	case reflect.Pointer:
		s := g.of(t.Elem())
		if ty, ok := s["type"].(string); ok {
			s["type"] = []any{ty, "null"}
			return s
		}
		return Schema{"anyOf": []any{s, Schema{"type": "null"}}}
	case reflect.Bool:
		return Schema{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return Schema{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return Schema{"type": "number"}
	case reflect.String:
		return Schema{"type": "string"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return Schema{"type": "string", "contentEncoding": "base64"}
		}
		return Schema{"type": []any{"array", "null"}, "items": g.of(t.Elem())}
	case reflect.Map:
		return Schema{"type": []any{"object", "null"}, "additionalProperties": g.of(t.Elem())}
	case reflect.Interface:
		return Schema{}
	case reflect.Struct:
		name := defName(t)
		if t.Name() == "" {
			return g.object(t)
		}
		if _, ok := g.defs[name]; !ok {
			g.defs[name] = Schema{} // break recursion
			g.defs[name] = g.object(t)
		}
		return Schema{"$ref": "#/$defs/" + name}
	}
	return Schema{}
}

func (g *gen) object(t reflect.Type) Schema {
	props := Schema{}
	var required []any
	g.fields(t, props, &required)
	s := Schema{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		sort.Slice(required, func(i, j int) bool { return required[i].(string) < required[j].(string) })
		s["required"] = required
	}
	return s
}

func (g *gen) fields(t reflect.Type, props Schema, required *[]any) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" || (!f.IsExported() && !f.Anonymous) {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				g.fields(ft, props, required)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		props[name] = g.of(f.Type)
		if !strings.Contains(opts, "omitempty") {
			*required = append(*required, name)
		}
	}
}

// Validate checks a decoded JSON document (decoded with UseNumber) against
// schema root.
func Validate(root Schema, doc any) error {
	return validate(root, root, doc, "$")
}

func validate(root, s Schema, v any, path string) error {
	if ref, ok := s["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		defs, _ := root["$defs"].(map[string]any)
		def, ok := defs[name].(map[string]any)
		if !ok {
			return fmt.Errorf("%s: unresolved %s", path, ref)
		}
		return validate(root, def, v, path)
	}
	if alts, ok := s["anyOf"].([]any); ok {
		for _, alt := range alts {
			if validate(root, alt.(map[string]any), v, path) == nil {
				return nil
			}
		}
		return fmt.Errorf("%s: matches no alternative", path)
	}
	if t, ok := s["type"]; ok && !typeMatches(t, v) {
		return fmt.Errorf("%s: %s is not of type %v", path, kindOf(v), t)
	}
	switch val := v.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if _, ok := val[r.(string)]; !ok {
					return fmt.Errorf("%s: missing required %q", path, r)
				}
			}
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if ps, ok := props[k].(map[string]any); ok {
				if err := validate(root, ps, val[k], path+"."+k); err != nil {
					return err
				}
				continue
			}
			switch ap := s["additionalProperties"].(type) {
			case bool:
				if !ap {
					return fmt.Errorf("%s: unexpected property %q", path, k)
				}
			case map[string]any:
				if err := validate(root, ap, val[k], path+"."+k); err != nil {
					return err
				}
			}
		}
	case []any:
		if items, ok := s["items"].(map[string]any); ok {
			for i, e := range val {
				if err := validate(root, items, e, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func kindOf(v any) string {
	switch n := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		if _, err := n.Int64(); err == nil || !strings.ContainsAny(n.String(), ".eE") {
			return "integer"
		}
		return "number"
	case float64:
		if n == float64(int64(n)) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

func typeMatches(t, v any) bool {
	k := kindOf(v)
	match := func(want string) bool { return want == k || (want == "number" && k == "integer") }
	switch tt := t.(type) {
	case string:
		return match(tt)
	case []any:
		for _, w := range tt {
			if match(w.(string)) {
				return true
			}
		}
	}
	return false
}

// Compatible lists the ways in which schema next breaks documents or
// readers built for schema prev: a property removed or retyped, or a new
// required property. Added optional properties are compatible.
func Compatible(prev, next Schema) []string {
	var out []string
	compat(prev, next, prev, next, "$", map[string]bool{}, &out)
	sort.Strings(out)
	return out
}

func resolve(root, s Schema) Schema {
	for {
		ref, ok := s["$ref"].(string)
		if !ok {
			return s
		}
		defs, _ := root["$defs"].(map[string]any)
		next, _ := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		if next == nil {
			return Schema{}
		}
		s = next
	}
}

func compat(rootP, rootN, p, n Schema, path string, seen map[string]bool, out *[]string) {
	if ref, ok := p["$ref"].(string); ok {
		if seen[ref+path] {
			return
		}
		seen[ref+path] = true
	}
	p, n = resolve(rootP, p), resolve(rootN, n)
	if fmt.Sprint(p["type"]) != fmt.Sprint(n["type"]) {
		*out = append(*out, fmt.Sprintf("%s: type changed from %v to %v", path, p["type"], n["type"]))
		return
	}
	pp, _ := p["properties"].(map[string]any)
	np, _ := n["properties"].(map[string]any)
	for name, ps := range pp {
		ns, ok := np[name]
		if !ok {
			*out = append(*out, fmt.Sprintf("%s.%s: removed", path, name))
			continue
		}
		compat(rootP, rootN, ps.(map[string]any), ns.(map[string]any), path+"."+name, seen, out)
	}
	was := map[string]bool{}
	if req, ok := p["required"].([]any); ok {
		for _, r := range req {
			was[r.(string)] = true
		}
	}
	if req, ok := n["required"].([]any); ok {
		for _, r := range req {
			// Documents written under prev may lack it.
			if !was[r.(string)] {
				*out = append(*out, fmt.Sprintf("%s.%s: newly required", path, r))
			}
		}
	}
	if pi, ok := p["items"].(map[string]any); ok {
		if ni, ok := n["items"].(map[string]any); ok {
			compat(rootP, rootN, pi, ni, path+"[]", seen, out)
		}
	}
	if pa, ok := p["additionalProperties"].(map[string]any); ok {
		if na, ok := n["additionalProperties"].(map[string]any); ok {
			compat(rootP, rootN, pa, na, path+"{}", seen, out)
		}
	}
}
