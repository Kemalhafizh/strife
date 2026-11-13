package history

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ednVal represents a parsed Clojure/Jepsen EDN value.
type ednKind uint8

const (
	ednNil ednKind = iota
	ednInt
	ednStr
	ednKeyword
	ednVec
	ednSet
	ednMap
)

type ednValue struct {
	kind ednKind
	i    int64
	s    string
	vec  []ednValue
	m    map[string]ednValue
}

func (v ednValue) asString() string {
	switch v.kind {
	case ednStr, ednKeyword:
		return v.s
	case ednInt:
		return strconv.FormatInt(v.i, 10)
	}
	return ""
}

func (v ednValue) asIntPtr() *int64 {
	if v.kind == ednInt {
		val := v.i
		return &val
	}
	return nil
}

func (v ednValue) asIntSlice() ([]int64, bool) {
	if v.kind != ednVec && v.kind != ednSet {
		return nil, false
	}
	out := make([]int64, 0, len(v.vec))
	for _, elem := range v.vec {
		if elem.kind != ednInt {
			return nil, false
		}
		out = append(out, elem.i)
	}
	return out, true
}

// ReadEDN parses a Jepsen EDN history stream (either newline-delimited EDN maps
// or a top-level vector of EDN maps). Non-client events such as `:process :nemesis`
// are ignored so histories exported directly from Jepsen runs can be checked.
func ReadEDN(r io.Reader) ([]Op, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	p := &ednParser{src: string(raw)}
	var vals []ednValue
	for {
		if err := p.skipWS(); err != nil {
			return nil, err
		}
		if p.eof() {
			break
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		if v.kind == ednVec {
			vals = append(vals, v.vec...)
		} else {
			vals = append(vals, v)
		}
	}

	var ops []Op
	nextProcess := 10000
	symProc := make(map[string]int)

	for idx, v := range vals {
		if v.kind != ednMap {
			continue
		}
		typStr := strings.ToLower(v.m["type"].asString())
		if typStr == "" {
			continue
		}
		var typ Type
		switch typStr {
		case "invoke":
			typ = Invoke
		case "ok":
			typ = OK
		case "fail":
			typ = Fail
		case "info":
			typ = Info
		default:
			continue
		}

		procVal, hasProc := v.m["process"]
		if !hasProc {
			continue
		}
		var proc int
		if procVal.kind == ednInt {
			proc = int(procVal.i)
		} else {
			ps := procVal.asString()
			if ps == "nemesis" {
				continue
			}
			pID, ok := symProc[ps]
			if !ok {
				pID = nextProcess
				nextProcess++
				symProc[ps] = pID
			}
			proc = pID
		}

		fName := normalizeEdnF(v.m["f"].asString())
		op := Op{
			Index:   idx,
			Process: proc,
			Type:    typ,
			F:       fName,
		}
		if tVal, ok := v.m["time"]; ok && tVal.kind == ednInt {
			op.Time = tVal.i
		}
		if kVal, ok := v.m["key"]; ok && kVal.kind != ednNil {
			op.Key = kVal.asString()
		}
		if nVal, ok := v.m["node"]; ok && nVal.kind != ednNil {
			op.Node = nVal.asString()
		}
		if eVal, ok := v.m["error"]; ok && eVal.kind != ednNil {
			op.Error = eVal.asString()
		}

		if val, ok := v.m["value"]; ok {
			applyEdnValue(&op, val)
		}
		if amtVal, ok := v.m["amount"]; ok && amtVal.kind == ednInt && op.Value == nil {
			op.Value = amtVal.asIntPtr()
		}
		ops = append(ops, op)
	}

	sort.SliceStable(ops, func(i, j int) bool { return ops[i].Time < ops[j].Time })
	for i := range ops {
		ops[i].Index = i
	}
	return ops, nil
}

func normalizeEdnF(f string) string {
	f = strings.ToLower(strings.TrimSpace(f))
	switch f {
	case "cas", "compare-and-set", "compare_and_set":
		return FCas
	case "read", "get", "r":
		return FRead
	case "write", "put", "set", "w":
		return FWrite
	case "add":
		return FAdd
	case "enqueue", "push":
		return FEnqueue
	case "dequeue", "pop", "poll":
		return FDequeue
	case "inc", "increment":
		return FInc
	case "txn", "transaction":
		return FTxn
	}
	return f
}

func applyEdnValue(op *Op, val ednValue) {
	if op.F == FTxn && val.kind == ednVec {
		for _, mv := range val.vec {
			if mv.kind != ednVec || len(mv.vec) < 2 {
				continue
			}
			mf := strings.ToLower(mv.vec[0].asString())
			switch mf {
			case "r", "read":
				mf = MicroRead
			case "w", "write":
				mf = MicroWrite
			case "append":
				mf = MicroAppend
			}
			m := MicroOp{
				F:   mf,
				Key: mv.vec[1].asString(),
			}
			if len(mv.vec) >= 3 {
				arg := mv.vec[2]
				if arg.kind == ednInt {
					m.Value = arg.asIntPtr()
				} else if list, ok := arg.asIntSlice(); ok {
					m.Values = list
				} else if arg.kind == ednVec && len(arg.vec) == 0 {
					m.Values = []int64{}
				}
			}
			op.Txn = append(op.Txn, m)
		}
		return
	}

	if op.F == FCas && val.kind == ednVec {
		// Either [old new] or [key [old new]]
		if len(val.vec) == 2 && val.vec[1].kind == ednVec && len(val.vec[1].vec) == 2 {
			if op.Key == "" {
				op.Key = val.vec[0].asString()
			}
			pair := val.vec[1].vec
			op.Old = pair[0].asIntPtr()
			op.Value = pair[1].asIntPtr()
			return
		}
		if len(val.vec) == 2 {
			op.Old = val.vec[0].asIntPtr()
			op.Value = val.vec[1].asIntPtr()
			return
		}
	}

	// Jepsen independent-key tuple: [key value]
	if val.kind == ednVec && len(val.vec) == 2 && op.Key == "" &&
		(val.vec[0].kind == ednKeyword || val.vec[0].kind == ednStr || (val.vec[0].kind == ednInt && op.F != FRead)) {
		// Distinguish a 2-element set/list read [1 2] from an independent tuple [:k0 1]
		if val.vec[0].kind == ednKeyword || val.vec[0].kind == ednStr || val.vec[1].kind == ednVec || val.vec[1].kind == ednNil {
			op.Key = val.vec[0].asString()
			applyEdnScalarOrSet(op, val.vec[1])
			return
		}
	}

	applyEdnScalarOrSet(op, val)
}

func applyEdnScalarOrSet(op *Op, val ednValue) {
	switch val.kind {
	case ednInt:
		op.Value = val.asIntPtr()
	case ednSet, ednVec:
		if list, ok := val.asIntSlice(); ok {
			op.Values = list
		}
	}
}

const maxEDNDepth = 256

type ednParser struct {
	src   string
	pos   int
	depth int
}

func (p *ednParser) eof() bool {
	return p.pos >= len(p.src)
}

func (p *ednParser) skipWS() error {
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' || ch == ',' {
			p.pos++
			continue
		}
		if ch == ';' {
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
			continue
		}
		if ch == '#' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '_' {
			p.pos += 2
			if _, err := p.parseValue(); err != nil {
				return err
			}
			continue
		}
		break
	}
	return nil
}

func (p *ednParser) parseValue() (ednValue, error) {
	if p.depth >= maxEDNDepth {
		return ednValue{}, fmt.Errorf("edn: maximum nesting depth (%d) exceeded", maxEDNDepth)
	}
	p.depth++
	defer func() { p.depth-- }()

	if err := p.skipWS(); err != nil {
		return ednValue{}, err
	}
	if p.eof() {
		return ednValue{}, fmt.Errorf("edn: unexpected EOF")
	}
	ch := p.src[p.pos]
	switch ch {
	case '{':
		return p.parseMap()
	case '[':
		return p.parseVec(']', ednVec)
	case '(':
		return p.parseVec(')', ednVec)
	case '"':
		return p.parseString()
	case ':':
		p.pos++
		tok := p.readToken()
		return ednValue{kind: ednKeyword, s: tok}, nil
	case '#':
		p.pos++
		if p.eof() {
			return ednValue{}, fmt.Errorf("edn: unexpected EOF after '#'")
		}
		if p.src[p.pos] == '{' {
			return p.parseVec('}', ednSet)
		}
		// Reader tag like #inst "..." or #uuid "..."
		_ = p.readToken()
		return p.parseValue()
	default:
		tok := p.readToken()
		if tok == "" {
			return ednValue{}, fmt.Errorf("edn: unexpected character %q at offset %d", ch, p.pos)
		}
		if tok == "nil" {
			return ednValue{kind: ednNil}, nil
		}
		if n, err := strconv.ParseInt(tok, 10, 64); err == nil {
			return ednValue{kind: ednInt, i: n}, nil
		}
		return ednValue{kind: ednKeyword, s: tok}, nil
	}
}

func (p *ednParser) parseMap() (ednValue, error) {
	p.pos++ // skip '{'
	m := make(map[string]ednValue)
	for {
		if err := p.skipWS(); err != nil {
			return ednValue{}, err
		}
		if p.eof() {
			return ednValue{}, fmt.Errorf("edn: unterminated map")
		}
		if p.src[p.pos] == '}' {
			p.pos++
			return ednValue{kind: ednMap, m: m}, nil
		}
		k, err := p.parseValue()
		if err != nil {
			return ednValue{}, err
		}
		v, err := p.parseValue()
		if err != nil {
			return ednValue{}, err
		}
		m[k.asString()] = v
	}
}

func (p *ednParser) parseVec(closeCh byte, kind ednKind) (ednValue, error) {
	p.pos++ // skip opening delimiter
	var vec []ednValue
	for {
		if err := p.skipWS(); err != nil {
			return ednValue{}, err
		}
		if p.eof() {
			return ednValue{}, fmt.Errorf("edn: unterminated sequence (want %q)", closeCh)
		}
		if p.src[p.pos] == closeCh {
			p.pos++
			return ednValue{kind: kind, vec: vec}, nil
		}
		elem, err := p.parseValue()
		if err != nil {
			return ednValue{}, err
		}
		vec = append(vec, elem)
	}
}

func (p *ednParser) parseString() (ednValue, error) {
	p.pos++ // skip '"'
	var b strings.Builder
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		p.pos++
		if ch == '"' {
			return ednValue{kind: ednStr, s: b.String()}, nil
		}
		if ch == '\\' && p.pos < len(p.src) {
			esc := p.src[p.pos]
			p.pos++
			switch esc {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(esc)
			}
			continue
		}
		b.WriteByte(ch)
	}
	return ednValue{}, fmt.Errorf("edn: unterminated string")
}

func (p *ednParser) readToken() string {
	start := p.pos
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' || ch == ',' ||
			ch == '{' || ch == '}' || ch == '[' || ch == ']' || ch == '(' || ch == ')' ||
			ch == '"' || ch == ';' {
			break
		}
		if !unicode.IsPrint(rune(ch)) {
			break
		}
		p.pos++
	}
	return p.src[start:p.pos]
}

// WriteEDN serializes a history slice into newline-delimited Jepsen EDN maps.
// The output is losslessly readable by ReadEDN and ReadJSONL.
func WriteEDN(w io.Writer, ops []Op) error {
	var b strings.Builder
	for i, op := range ops {
		b.Reset()
		idx := op.Index
		if idx == 0 && i > 0 {
			idx = i
		}
		fmt.Fprintf(&b, "{:index %d, :type :%s, :f :%s, :process %d, :time %d",
			idx, string(op.Type), op.F, op.Process, op.Time)
		if op.Key != "" {
			fmt.Fprintf(&b, ", :key %s", quoteEDNString(op.Key))
		}
		if op.Node != "" {
			fmt.Fprintf(&b, ", :node %s", quoteEDNString(op.Node))
		}
		if op.Error != "" {
			fmt.Fprintf(&b, ", :error %s", quoteEDNString(op.Error))
		}
		switch {
		case op.F == FTxn:
			if op.Value != nil {
				fmt.Fprintf(&b, ", :amount %d", *op.Value)
			}
			b.WriteString(", :value [")
			for j, m := range op.Txn {
				if j > 0 {
					b.WriteByte(' ')
				}
				fmt.Fprintf(&b, "[:%s %s ", m.F, quoteEDNString(m.Key))
				if m.Values != nil {
					b.WriteByte('[')
					for k, v := range m.Values {
						if k > 0 {
							b.WriteByte(' ')
						}
						fmt.Fprintf(&b, "%d", v)
					}
					b.WriteByte(']')
				} else if m.Value != nil {
					fmt.Fprintf(&b, "%d", *m.Value)
				} else {
					b.WriteString("nil")
				}
				b.WriteByte(']')
			}
			b.WriteByte(']')
		case op.F == FCas && op.Old != nil && op.Value != nil:
			fmt.Fprintf(&b, ", :value [%d %d]", *op.Old, *op.Value)
		case op.Values != nil:
			b.WriteString(", :value [")
			for j, v := range op.Values {
				if j > 0 {
					b.WriteByte(' ')
				}
				fmt.Fprintf(&b, "%d", v)
			}
			b.WriteByte(']')
		case op.Value != nil:
			fmt.Fprintf(&b, ", :value %d", *op.Value)
		default:
			b.WriteString(", :value nil")
		}
		b.WriteString("}\n")
		if _, err := io.WriteString(w, b.String()); err != nil {
			return err
		}
	}
	return nil
}

func quoteEDNString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('"')
	return b.String()
}
