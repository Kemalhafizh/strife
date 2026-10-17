// Package probe collects server-side kernel, storage, process, and clock
// telemetry (such as fsync stalls, Linux PSI pressure, cgroup OOM kills,
// signals, and clock jumps) and correlates them with client operation histories
// to explain why a consistency violation occurred.
package probe

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

// EventKind identifies the class of server-side diagnostic observation.
type EventKind string

// Supported server-side diagnostic event kinds.
const (
	KindFsyncStall    EventKind = "fsync_stall"
	KindFsyncError    EventKind = "fsync_error"
	KindOOMKill       EventKind = "oom_kill"
	KindProcessSignal EventKind = "process_signal"
	KindIOPressure    EventKind = "io_pressure"
	KindCPUPressure   EventKind = "cpu_pressure"
	KindClockJump     EventKind = "clock_jump"
)

// Event represents one server-side observation recorded on a cluster node.
// Start and End are relative timestamps in nanoseconds from the start of the
// test run (matching history.Op.Time).
type Event struct {
	ID         int       `json:"id,omitempty"`
	Node       string    `json:"node"`
	Kind       EventKind `json:"kind"`
	AtNs       int64     `json:"at_ns,omitempty"`
	Start      int64     `json:"start"`
	End        int64     `json:"end"`
	DurationNs int64     `json:"duration_ns,omitempty"`
	PID        int       `json:"pid,omitempty"`
	Process    string    `json:"process,omitempty"`
	Signal     string    `json:"signal,omitempty"`
	Errno      string    `json:"errno,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

// Duration returns the duration of the event as a time.Duration.
func (e Event) Duration() time.Duration {
	if e.DurationNs > 0 {
		return time.Duration(e.DurationNs)
	}
	if e.End > e.Start {
		return time.Duration(e.End - e.Start)
	}
	return 0
}

// Summary formats a concise one-line human description of the probe event.
func (e Event) Summary() string {
	node := e.Node
	if node == "" {
		node = "node"
	}
	dur := e.Duration()
	switch e.Kind {
	case KindFsyncStall:
		if e.Detail != "" {
			return fmt.Sprintf("%s: fsync stalled for %v (%s)", node, dur.Round(time.Millisecond), e.Detail)
		}
		return fmt.Sprintf("%s: fsync stalled for %v", node, dur.Round(time.Millisecond))
	case KindFsyncError:
		return fmt.Sprintf("%s: fsync failed (%s %s)", node, e.Errno, e.Detail)
	case KindOOMKill:
		if e.Process != "" {
			return fmt.Sprintf("%s: OOM killer terminated %s (pid %d)", node, e.Process, e.PID)
		}
		return fmt.Sprintf("%s: kernel/cgroup OOM kill (%s)", node, e.Detail)
	case KindProcessSignal:
		if e.Process != "" {
			return fmt.Sprintf("%s: process %s (pid %d) received %s", node, e.Process, e.PID, e.Signal)
		}
		return fmt.Sprintf("%s: signal %s (%s)", node, e.Signal, e.Detail)
	case KindIOPressure:
		return fmt.Sprintf("%s: Linux PSI I/O stall %v (%s)", node, dur.Round(time.Millisecond), e.Detail)
	case KindCPUPressure:
		return fmt.Sprintf("%s: Linux PSI CPU stall %v (%s)", node, dur.Round(time.Millisecond), e.Detail)
	case KindClockJump:
		return fmt.Sprintf("%s: clock jump %v (%s)", node, dur.Round(time.Millisecond), e.Detail)
	default:
		return fmt.Sprintf("%s: %s (%s)", node, e.Kind, e.Detail)
	}
}

// ReadEvents parses JSON Lines of server-side probe events from r.
func ReadEvents(r io.Reader) ([]Event, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var out []Event
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("probe line %d: %w", lineNo, err)
		}
		if ev.Start == 0 && ev.AtNs > 0 {
			ev.Start = ev.AtNs
		}
		if ev.AtNs == 0 && ev.Start > 0 {
			ev.AtNs = ev.Start
		}
		if ev.End == 0 && ev.DurationNs > 0 {
			ev.End = ev.Start + ev.DurationNs
		}
		if ev.DurationNs == 0 && ev.End > ev.Start {
			ev.DurationNs = ev.End - ev.Start
		}
		if ev.ID == 0 {
			ev.ID = len(out) + 1
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out, nil
}

// LoadEvents reads a JSONL file of probe events from path.
func LoadEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadEvents(f)
}

// WriteEvents serializes probe events as JSON Lines to w.
func WriteEvents(w io.Writer, events []Event) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			return err
		}
	}
	return bw.Flush()
}
