// Package history records and manipulates the operation histories that the
// consistency checkers consume.
//
// The format follows Jepsen: every client operation produces an "invoke"
// event and later exactly one completion event of type "ok" (it definitely
// happened), "fail" (it definitely did not happen) or "info" (outcome
// unknown, e.g. a timeout). A process that observes "info" never issues
// another operation; the client continues under a fresh process id, because
// the indeterminate operation may still be in flight.
package history

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
)

// Type is the kind of a history event.
type Type string

// Event types.
const (
	Invoke Type = "invoke"
	OK     Type = "ok"
	Fail   Type = "fail"
	Info   Type = "info"
)

// Operation functions understood by the built-in models.
const (
	FRead     = "read"
	FWrite    = "write"
	FCas      = "cas"
	FAdd      = "add"
	FEnqueue  = "enqueue"
	FDequeue  = "dequeue"
	FInc      = "inc"
	FGenerate = "generate"
	FTxn      = "txn"
)

// MicroOp is one operation inside a transaction.
type MicroOp struct {
	// F is "r" (read), "w" (write a register) or "append" (append Value to
	// the list stored at Key).
	F     string `json:"f"`
	Key   string `json:"key"`
	Value *int64 `json:"value,omitempty"` // written, appended or read register value
	// Values is the list a read of a list-append key returned, filled in on
	// the completion event (nil and empty are both the empty list).
	Values []int64 `json:"values,omitempty"`
}

// Micro-operation functions.
const (
	MicroRead   = "r"
	MicroWrite  = "w"
	MicroAppend = "append"
)

// Op is one event in a history.
type Op struct {
	Index   int       `json:"index"`
	Process int       `json:"process"`
	Type    Type      `json:"type"`
	F       string    `json:"f"`
	Key     string    `json:"key,omitempty"`
	Value   *int64    `json:"value,omitempty"`  // write/add argument, cas new value, read result
	Old     *int64    `json:"old,omitempty"`    // cas expected value
	Values  []int64   `json:"values,omitempty"` // set read result
	Txn     []MicroOp `json:"txn,omitempty"`    // transactional micro-operations
	Time    int64     `json:"time"`             // nanoseconds since the start of the test
	Node    string    `json:"node,omitempty"`   // which node served the request, if known
	Error   string    `json:"error,omitempty"`
}

// Int returns a pointer to v; handy for building ops.
func Int(v int64) *int64 { return &v }

// String renders an op compactly, e.g. "p3 cas k1 2→5".
func (o Op) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "p%d %s", o.Process, o.F)
	if o.Key != "" {
		fmt.Fprintf(&b, " %s", o.Key)
	}
	switch o.F {
	case FCas:
		fmt.Fprintf(&b, " %s→%s", fmtVal(o.Old), fmtVal(o.Value))
	case FTxn:
		b.WriteString(" [")
		for i, m := range o.Txn {
			if i > 0 {
				b.WriteString(", ")
			}
			if m.F == "w" || m.F == "write" {
				fmt.Fprintf(&b, "w(%s, %s)", m.Key, fmtVal(m.Value))
			} else if m.F == MicroAppend {
				fmt.Fprintf(&b, "append(%s, %s)", m.Key, fmtVal(m.Value))
			} else if m.Values != nil {
				fmt.Fprintf(&b, "r(%s)→%v", m.Key, m.Values)
			} else {
				if m.Value != nil {
					fmt.Fprintf(&b, "r(%s)→%s", m.Key, fmtVal(m.Value))
				} else {
					fmt.Fprintf(&b, "r(%s)", m.Key)
				}
			}
		}
		b.WriteString("]")
	case FRead, FDequeue, FGenerate:
		if o.Type != Invoke {
			if o.Values != nil {
				fmt.Fprintf(&b, " →%v", o.Values)
			} else {
				fmt.Fprintf(&b, " →%s", fmtVal(o.Value))
			}
		}
	default:
		fmt.Fprintf(&b, " %s", fmtVal(o.Value))
	}
	return b.String()
}

func fmtVal(v *int64) string {
	if v == nil {
		return "nil"
	}
	return fmt.Sprint(*v)
}

// Infinity is the return time assigned to operations whose outcome is unknown.
const Infinity = math.MaxInt64

// Operation is an invocation paired with its completion.
type Operation struct {
	ID      int   // position in the slice returned by Pair
	Process int   // process that issued it
	Call    int64 // invocation time
	Return  int64 // completion time, Infinity when unknown
	Input   Op    // the invoke event
	Output  Op    // the completion event (Type Info when unknown or missing)
}

// Micro returns the transaction's micro-operations with what they observed:
// the completion's when it reports results for every micro-op (the Jepsen
// convention), the invocation's otherwise.
func (o Operation) Micro() []MicroOp {
	if len(o.Output.Txn) == len(o.Input.Txn) && o.Output.Type == OK {
		return o.Output.Txn
	}
	return o.Input.Txn
}

// Status is the completion type of the operation.
func (o Operation) Status() Type { return o.Output.Type }

// Describe renders the operation with its observed result.
func (o Operation) Describe() string {
	in := o.Input
	switch in.F {
	case FRead:
		if o.Output.Type != OK {
			return fmt.Sprintf("p%d read %s → ?", o.Process, in.Key)
		}
		if o.Output.Values != nil || in.Key == "" {
			return fmt.Sprintf("p%d read → %v", o.Process, o.Output.Values)
		}
		return fmt.Sprintf("p%d read %s → %s", o.Process, in.Key, fmtVal(o.Output.Value))
	case FCas:
		return fmt.Sprintf("p%d cas %s %s→%s (%s)", o.Process, in.Key, fmtVal(in.Old), fmtVal(in.Value), o.Output.Type)
	case FTxn:
		return fmt.Sprintf("%s (%s)", Op{Process: o.Process, F: FTxn, Txn: o.Micro()}.String(), o.Output.Type)
	default:
		k := in.Key
		if k != "" {
			k += " "
		}
		return fmt.Sprintf("p%d %s %s%s (%s)", o.Process, in.F, k, fmtVal(in.Value), o.Output.Type)
	}
}

// Pair matches every invocation with its completion. Invocations that never
// completed are treated as "info" with an infinite return time. It returns an
// error if a process issues a second invocation while one is outstanding.
func Pair(ops []Op) ([]Operation, error) {
	open := map[int]int{} // process -> index into out
	var out []Operation
	for _, op := range ops {
		switch op.Type {
		case Invoke:
			if _, busy := open[op.Process]; busy {
				return nil, fmt.Errorf("history: process %d invoked op %d while another op is outstanding", op.Process, op.Index)
			}
			open[op.Process] = len(out)
			out = append(out, Operation{ID: len(out), Process: op.Process, Call: op.Time, Return: Infinity, Input: op})
		case OK, Fail, Info:
			i, busy := open[op.Process]
			if !busy {
				return nil, fmt.Errorf("history: completion %d for process %d without invocation", op.Index, op.Process)
			}
			delete(open, op.Process)
			out[i].Output = op
			if op.Type != Info {
				out[i].Return = op.Time
			}
		default:
			return nil, fmt.Errorf("history: op %d has unknown type %q", op.Index, op.Type)
		}
	}
	for _, i := range open {
		out[i].Output = Op{Process: out[i].Process, Type: Info, F: out[i].Input.F, Key: out[i].Input.Key, Time: Infinity}
	}
	return out, nil
}

// Stats summarises a history.
type Stats struct {
	Invocations int                    `json:"invocations"`
	OK          int                    `json:"ok"`
	Fail        int                    `json:"fail"`
	Info        int                    `json:"info"`
	ByF         map[string]*StatsCount `json:"by_f"`
}

// StatsCount counts outcomes for one function.
type StatsCount struct {
	OK   int `json:"ok"`
	Fail int `json:"fail"`
	Info int `json:"info"`
}

// Summarize computes Stats for paired operations.
func Summarize(ops []Operation) Stats {
	s := Stats{ByF: map[string]*StatsCount{}}
	for _, o := range ops {
		s.Invocations++
		c := s.ByF[o.Input.F]
		if c == nil {
			c = &StatsCount{}
			s.ByF[o.Input.F] = c
		}
		switch o.Status() {
		case OK:
			s.OK++
			c.OK++
		case Fail:
			s.Fail++
			c.Fail++
		default:
			s.Info++
			c.Info++
		}
	}
	return s
}

// Recorder appends ops to a history with consistent indices and timestamps.
// It is safe for concurrent use.
type Recorder struct {
	mu    sync.Mutex
	clock func() int64
	ops   []Op
}

// NewRecorder returns a recorder that timestamps events with clock.
func NewRecorder(clock func() int64) *Recorder { return &Recorder{clock: clock} }

// Record stamps op with an index and the current time and appends it.
func (r *Recorder) Record(op Op) Op {
	r.mu.Lock()
	defer r.mu.Unlock()
	op.Index = len(r.ops)
	op.Time = r.clock()
	r.ops = append(r.ops, op)
	return op
}

// Ops returns a copy of the recorded history.
func (r *Recorder) Ops() []Op {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Op(nil), r.ops...)
}

// WriteJSONL writes one JSON object per line.
func WriteJSONL(w io.Writer, ops []Op) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	for _, op := range ops {
		if err := enc.Encode(op); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// ReadJSONL parses a history written by WriteJSONL. Indices are reassigned in
// file order and the result is sorted by time (stable) to tolerate histories
// merged from several clients.
func ReadJSONL(r io.Reader) ([]Op, error) {
	var ops []Op
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		var op Op
		if err := json.Unmarshal([]byte(text), &op); err != nil {
			return nil, fmt.Errorf("history: line %d: %w", line, err)
		}
		ops = append(ops, op)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(ops, func(i, j int) bool { return ops[i].Time < ops[j].Time })
	for i := range ops {
		ops[i].Index = i
	}
	return ops, nil
}

// Hash returns the SHA-256 of the canonical JSONL encoding of ops.
func Hash(ops []Op) string {
	h := sha256.New()
	_ = WriteJSONL(h, ops)
	return hex.EncodeToString(h.Sum(nil))
}
