// Package nemesis generates deterministic fault schedules and turns them into
// concrete actions: simulated faults inside the deterministic simulator, or
// iptables / tc / cgroup / signal commands against real machines.
package nemesis

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/internal/rng"
)

// Kind is a fault type.
type Kind string

// Fault kinds.
const (
	Partition Kind = "partition" // iptables DROP between two sides of the cluster
	Latency   Kind = "latency"   // tc netem delay + jitter
	Loss      Kind = "loss"      // tc netem packet loss
	Kill      Kind = "kill"      // SIGKILL the process, restart it when the fault ends
	Pause     Kind = "pause"     // SIGSTOP / SIGCONT
	Freeze    Kind = "freeze"    // cgroup v2 freezer
	IOStall   Kind = "io-stall"  // cgroup v2 io.max throttled to ~0: fsync blocks
	ClockSkew Kind = "clock-skew"
)

// AllKinds lists every fault kind.
var AllKinds = []Kind{Partition, Latency, Loss, Kill, Pause, Freeze, IOStall, ClockSkew}

// ParseKinds parses a comma separated list; "all" and "none" are accepted.
func ParseKinds(s string) ([]Kind, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "", "none":
		return nil, nil
	case "all":
		return append([]Kind(nil), AllKinds...), nil
	}
	var out []Kind
	seen := map[Kind]bool{}
	for _, part := range strings.Split(s, ",") {
		k := Kind(strings.TrimSpace(part))
		valid := false
		for _, a := range AllKinds {
			if a == k {
				valid = true
			}
		}
		if !valid {
			return nil, fmt.Errorf("nemesis: unknown fault %q (valid: %s)", k, joinKinds(AllKinds))
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, nil
}

func joinKinds(ks []Kind) string {
	s := make([]string, len(ks))
	for i, k := range ks {
		s[i] = string(k)
	}
	return strings.Join(s, ",")
}

// lane groups kinds that cannot overlap in time because they share an
// underlying mechanism (one netem qdisc per interface, one process state...).
func lane(k Kind) string {
	switch k {
	case Latency, Loss:
		return "netem"
	case Kill, Pause, Freeze:
		return "process"
	}
	return string(k)
}

// Fault is one scheduled fault window.
type Fault struct {
	ID    int           `json:"id"`
	Kind  Kind          `json:"kind"`
	Start time.Duration `json:"start"`
	End   time.Duration `json:"end"`
	// Nodes are the affected node indices. For a partition they are one side;
	// the other side is every remaining node.
	Nodes []int `json:"nodes"`
	// Kind specific parameters.
	Delay  time.Duration `json:"delay,omitempty"`  // latency
	Jitter time.Duration `json:"jitter,omitempty"` // latency
	Loss   float64       `json:"loss,omitempty"`   // loss probability, 0..1
	Rate   float64       `json:"rate,omitempty"`   // clock-skew: local clock speed multiplier
	Offset time.Duration `json:"offset,omitempty"` // clock-skew: wall clock jump (live runs)
}

// Other returns the complement of f.Nodes among n nodes.
func (f Fault) Other(n int) []int {
	in := map[int]bool{}
	for _, x := range f.Nodes {
		in[x] = true
	}
	var out []int
	for i := 0; i < n; i++ {
		if !in[i] {
			out = append(out, i)
		}
	}
	return out
}

// Describe renders f for humans.
func (f Fault) Describe(n int) string {
	switch f.Kind {
	case Partition:
		return fmt.Sprintf("partition %v | %v", f.Nodes, f.Other(n))
	case Latency:
		return fmt.Sprintf("latency +%v±%v on %v", f.Delay, f.Jitter, f.Nodes)
	case Loss:
		return fmt.Sprintf("packet loss %.0f%% on %v", f.Loss*100, f.Nodes)
	case ClockSkew:
		return fmt.Sprintf("clock-skew ×%.2f (jump %v) on %v", f.Rate, f.Offset, f.Nodes)
	}
	return fmt.Sprintf("%s %v", f.Kind, f.Nodes)
}

// Options control schedule generation.
type Options struct {
	Nodes    int
	Duration time.Duration // total test length; faults end before 90% of it
	Kinds    []Kind
	Interval time.Duration // mean time between fault starts
	Length   time.Duration // mean fault length
}

// Schedule is a deterministic, replayable list of faults.
type Schedule struct {
	Seed     uint64        `json:"seed"`
	Nodes    int           `json:"nodes"`
	Duration time.Duration `json:"duration"`
	Faults   []Fault       `json:"faults"`
}

// Hash is the SHA-256 of the canonical JSON encoding of the schedule.
func (s Schedule) Hash() string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Generate builds a schedule from a seed. The same (seed, options) always
// yields the same schedule. Faults of kinds sharing a mechanism never overlap;
// different mechanisms may, which is where the interesting bugs live.
func Generate(seed uint64, o Options) Schedule {
	s := Schedule{Seed: seed, Nodes: o.Nodes, Duration: o.Duration}
	if o.Nodes <= 0 || len(o.Kinds) == 0 || o.Duration <= 0 {
		return s
	}
	if o.Interval <= 0 {
		o.Interval = o.Duration / 10
	}
	if o.Length <= 0 {
		o.Length = o.Interval
	}
	r := rng.New(seed).Fork("nemesis")
	horizon := o.Duration * 85 / 100
	busy := map[string]time.Duration{}
	t := o.Duration / 20
	for {
		// Exponential-ish gaps keep fault arrivals bursty but bounded.
		gap := time.Duration(float64(o.Interval) * (0.25 + 1.5*r.Float64()))
		t += gap
		if t >= horizon {
			break
		}
		// Pick a kind whose lane is free; give up on this slot otherwise.
		order := r.Perm(len(o.Kinds))
		var kind Kind
		for _, i := range order {
			if busy[lane(o.Kinds[i])] <= t {
				kind = o.Kinds[i]
				break
			}
		}
		if kind == "" {
			continue
		}
		length := time.Duration(float64(o.Length) * (0.4 + 1.2*r.Float64()))
		end := t + length
		if end > horizon {
			end = horizon
		}
		if end <= t {
			continue
		}
		f := Fault{ID: len(s.Faults), Kind: kind, Start: t, End: end}
		fill(&f, r, o.Nodes)
		busy[lane(kind)] = end
		s.Faults = append(s.Faults, f)
	}
	return s
}

func fill(f *Fault, r *rng.Rand, n int) {
	perm := r.Perm(n)
	minority := n / 2
	if n%2 == 1 {
		minority = (n - 1) / 2
	}
	if minority < 1 {
		minority = 1
	}
	pick := func(k int) []int {
		out := append([]int(nil), perm[:k]...)
		sort.Ints(out)
		return out
	}
	switch f.Kind {
	case Partition:
		// Mostly isolate a minority; a single isolated node is the common
		// "lost leader" scenario.
		size := 1
		if minority > 1 && r.Bool(0.5) {
			size = 1 + r.Intn(minority)
		}
		f.Nodes = pick(size)
	case Latency:
		f.Nodes = pick(1 + r.Intn(n))
		f.Delay = time.Duration(r.Range(20, 400)) * time.Millisecond
		f.Jitter = f.Delay / 4
	case Loss:
		f.Nodes = pick(1 + r.Intn(n))
		f.Loss = float64(r.Range(10, 60)) / 100
	case Kill, Pause, Freeze, IOStall:
		// Usually a minority, sometimes a majority or the whole cluster
		// (a power cut): losing quorum must cost availability, never safety.
		size := 1 + r.Intn(minority)
		switch x := r.Float64(); {
		case x < 0.15:
			size = n
		case x < 0.35:
			size = minority + 1
		}
		if size > n {
			size = n
		}
		f.Nodes = pick(size)
	case ClockSkew:
		f.Nodes = pick(1 + r.Intn(minority))
		if r.Bool(0.5) {
			f.Rate = 2 + 6*r.Float64() // fast clock: timers fire early
		} else {
			f.Rate = 0.1 + 0.4*r.Float64() // slow clock: timers fire late
		}
		f.Rate = float64(int(f.Rate*100)) / 100
		f.Offset = time.Duration(r.Range(-30, 30)) * time.Second
	}
}

// Action is one edge of a fault window.
type Action struct {
	At    time.Duration
	Fault Fault
	Start bool // true: inject; false: heal
}

// Timeline flattens a schedule into start/stop actions ordered by time. At
// equal times heals come first, then starts in fault order.
func (s Schedule) Timeline() []Action {
	var out []Action
	for _, f := range s.Faults {
		out = append(out, Action{At: f.Start, Fault: f, Start: true}, Action{At: f.End, Fault: f, Start: false})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.At != b.At {
			return a.At < b.At
		}
		if a.Start != b.Start {
			return !a.Start
		}
		return a.Fault.ID < b.Fault.ID
	})
	return out
}
