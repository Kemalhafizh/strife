// Package sim is a deterministic discrete-event simulator for distributed
// systems, in the spirit of FoundationDB's simulation testing.
//
// Everything that is nondeterministic in a real deployment (message delays,
// drops, timer firing, process crashes, disk latency, clock drift) is drawn
// from a single seeded random stream and executed on a single thread in a
// total order. The same seed therefore replays the same execution bit for bit,
// and every execution is summarised by a trace hash that proves it.
package sim

import (
	"container/heap"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"time"

	"github.com/kemalhafizh/strife/internal/rng"
)

// NodeID identifies an actor. Server nodes are 0..N-1; clients use ClientBase+i.
type NodeID int

// ClientBase is the first client id. Network partitions, crashes and other
// server faults never apply to clients.
const ClientBase NodeID = 1000

// IsServer reports whether id is a server node.
func (id NodeID) IsServer() bool { return id >= 0 && id < ClientBase }

// Node is a simulated process.
type Node interface {
	// Start boots the node: initially and after every crash. It must reload
	// only durable state.
	Start()
	// Receive handles one message.
	Receive(from NodeID, msg any)
}

// Config tunes the simulated environment.
type Config struct {
	Seed        uint64
	MinLatency  time.Duration // one-way network delay lower bound
	MaxLatency  time.Duration // one-way network delay upper bound
	DiskLatency time.Duration // mean fsync latency
}

type eventKind uint8

const (
	evMessage eventKind = iota
	evTimer
	evFsync
	evFunc // world-level function (nemesis, harness)
)

type event struct {
	at    int64
	seq   uint64
	kind  eventKind
	node  NodeID
	epoch uint64
	from  NodeID
	msg   any
	fn    func()
	batch []func() // fsync: callbacks made durable by this write
}

type eventQueue []*event

func (q eventQueue) Len() int { return len(q) }
func (q eventQueue) Less(i, j int) bool {
	if q[i].at != q[j].at {
		return q[i].at < q[j].at
	}
	return q[i].seq < q[j].seq
}
func (q eventQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *eventQueue) Push(x any)   { *q = append(*q, x.(*event)) }
func (q *eventQueue) Pop() any {
	old := *q
	e := old[len(old)-1]
	*q = old[:len(old)-1]
	return e
}

type clock struct {
	localBase int64
	realBase  int64
	rate      float64
}

// World is the simulated universe.
type World struct {
	cfg   Config
	now   int64
	seq   uint64
	queue eventQueue
	net   *rng.Rand
	disk  *rng.Rand
	nodes map[NodeID]Node
	ids   []NodeID

	epoch   map[NodeID]uint64
	down    map[NodeID]bool
	frozen  map[NodeID]bool
	held    map[NodeID][]*event // events that arrived while frozen
	stalled map[NodeID]bool
	disks   map[NodeID]*diskState
	clocks  map[NodeID]*clock

	blocked map[[2]NodeID]int // directional link -> number of active partitions
	delay   map[NodeID]time.Duration
	jitter  map[NodeID]time.Duration
	loss    map[NodeID]float64

	trace  hash.Hash
	events uint64
	tracef func(format string, args ...any)
}

// NewWorld creates an empty world.
func NewWorld(cfg Config) *World {
	if cfg.MinLatency <= 0 {
		cfg.MinLatency = time.Millisecond
	}
	if cfg.MaxLatency < cfg.MinLatency {
		cfg.MaxLatency = cfg.MinLatency * 5
	}
	if cfg.DiskLatency <= 0 {
		cfg.DiskLatency = 2 * time.Millisecond
	}
	root := rng.New(cfg.Seed)
	return &World{
		cfg:     cfg,
		net:     root.Fork("sim/network"),
		disk:    root.Fork("sim/disk"),
		nodes:   map[NodeID]Node{},
		epoch:   map[NodeID]uint64{},
		down:    map[NodeID]bool{},
		frozen:  map[NodeID]bool{},
		held:    map[NodeID][]*event{},
		stalled: map[NodeID]bool{},
		disks:   map[NodeID]*diskState{},
		clocks:  map[NodeID]*clock{},
		blocked: map[[2]NodeID]int{},
		delay:   map[NodeID]time.Duration{},
		jitter:  map[NodeID]time.Duration{},
		loss:    map[NodeID]float64{},
		trace:   sha256.New(),
	}
}

// SetTracer installs a debug logger for every processed event.
func (w *World) SetTracer(f func(format string, args ...any)) { w.tracef = f }

// Add registers a node. Nodes must be added before Run.
func (w *World) Add(id NodeID, n Node) {
	w.nodes[id] = n
	w.ids = append(w.ids, id)
	w.clocks[id] = &clock{rate: 1}
}

// Now is the global (true) simulated time in nanoseconds.
func (w *World) Now() int64 { return w.now }

// LocalNow is node id's view of time, which drifts under clock skew.
func (w *World) LocalNow(id NodeID) int64 {
	c := w.clocks[id]
	if c == nil {
		return w.now
	}
	return c.localBase + int64(float64(w.now-c.realBase)*c.rate)
}

// Events is the number of events processed so far.
func (w *World) Events() uint64 { return w.events }

// TraceHash is the SHA-256 of every processed event, in order. Two runs with
// equal trace hashes executed identically.
func (w *World) TraceHash() string {
	return hex.EncodeToString(w.trace.Sum(nil))
}

func (w *World) push(e *event) {
	w.seq++
	e.seq = w.seq
	heap.Push(&w.queue, e)
}

// Send transmits msg from one actor to another through the simulated network.
func (w *World) Send(from, to NodeID, msg any) {
	if w.cut(from, to) {
		return
	}
	if p := 1 - (1-w.loss[from])*(1-w.loss[to]); p > 0 && w.net.Bool(p) {
		return
	}
	d := w.net.Range(int64(w.cfg.MinLatency), int64(w.cfg.MaxLatency)+1)
	for _, n := range []NodeID{from, to} {
		if extra := w.delay[n]; extra > 0 {
			j := int64(w.jitter[n])
			d += int64(extra) + w.net.Range(-j, j+1)
		}
	}
	if d < 0 {
		d = 0
	}
	w.push(&event{at: w.now + d, kind: evMessage, node: to, from: from, msg: msg})
}

func (w *World) cut(from, to NodeID) bool { return w.blocked[[2]NodeID{from, to}] > 0 }

// After runs fn on node id after d of the node's *local* time. Timers die
// with the node incarnation that set them.
func (w *World) After(id NodeID, d time.Duration, fn func()) {
	real := int64(d)
	if c := w.clocks[id]; c != nil && c.rate != 1 {
		real = int64(float64(d) / c.rate)
	}
	w.push(&event{at: w.now + real, kind: evTimer, node: id, epoch: w.epoch[id], fn: fn})
}

// At runs fn at absolute simulated time t outside of any node (harness and
// nemesis hooks).
func (w *World) At(t int64, fn func()) {
	if t < w.now {
		t = w.now
	}
	w.push(&event{at: t, kind: evFunc, node: -1, fn: fn})
}

type diskState struct {
	inflight bool
	queue    []func()
}

func (w *World) diskOf(id NodeID) *diskState {
	d := w.disks[id]
	if d == nil {
		d = &diskState{}
		w.disks[id] = d
	}
	return d
}

// Fsync makes the node's pending writes durable and then runs fn. Like a
// real storage engine it group-commits: writes issued while an fsync is in
// flight are flushed together by the next one, and callbacks run in FIFO
// order. A crash loses every write whose fsync has not completed; an I/O
// stall blocks completion until the stall ends.
func (w *World) Fsync(id NodeID, fn func()) {
	d := w.diskOf(id)
	d.queue = append(d.queue, fn)
	w.kickDisk(id)
}

func (w *World) kickDisk(id NodeID) {
	d := w.diskOf(id)
	if d.inflight || w.stalled[id] || w.down[id] || len(d.queue) == 0 {
		return
	}
	batch := d.queue
	d.queue = nil
	d.inflight = true
	mean := int64(w.cfg.DiskLatency)
	at := w.now + w.disk.Range(mean/2, mean*3/2+1)
	w.push(&event{at: at, kind: evFsync, node: id, epoch: w.epoch[id], batch: batch})
}

// Run processes events until the queue is empty or simulated time passes
// until (nanoseconds).
func (w *World) Run(until int64) {
	for len(w.queue) > 0 {
		e := heap.Pop(&w.queue).(*event)
		if e.at > until {
			heap.Push(&w.queue, e)
			w.now = until
			return
		}
		w.now = e.at
		w.dispatch(e)
	}
}

// Start boots every node in registration order.
func (w *World) Start() {
	for _, id := range w.ids {
		w.nodes[id].Start()
	}
}

func (w *World) record(e *event, what string) {
	w.events++
	fmt.Fprintf(w.trace, "%d|%d|%d|%d|%d|%s|%T|%+v\n", e.at, e.seq, e.kind, e.node, e.from, what, e.msg, e.msg)
	if w.tracef != nil {
		w.tracef("%8.3fms %-6s n%d<-n%d %T%+v", float64(e.at)/1e6, what, e.node, e.from, e.msg, e.msg)
	}
}

func (w *World) dispatch(e *event) {
	if e.kind == evFunc {
		w.record(e, "func")
		e.fn()
		return
	}
	if w.down[e.node] {
		w.record(e, "dead")
		return
	}
	if e.kind != evMessage && e.epoch != w.epoch[e.node] {
		w.record(e, "stale")
		return
	}
	if e.kind == evFsync && w.stalled[e.node] {
		// The write is stuck in the device; it completes after the stall.
		d := w.diskOf(e.node)
		d.inflight = false
		d.queue = append(e.batch, d.queue...)
		w.record(e, "stall")
		return
	}
	if w.frozen[e.node] {
		w.held[e.node] = append(w.held[e.node], e)
		w.record(e, "held")
		return
	}
	switch e.kind {
	case evMessage:
		if e.from.IsServer() && w.cut(e.from, e.node) {
			w.record(e, "cut")
			return
		}
		w.record(e, "recv")
		w.nodes[e.node].Receive(e.from, e.msg)
	case evTimer:
		w.record(e, "timer")
		e.fn()
	case evFsync:
		w.record(e, "fsync")
		w.diskOf(e.node).inflight = false
		for _, fn := range e.batch {
			fn()
		}
		w.kickDisk(e.node)
	}
}

// ---------------------------------------------------------------------------
// Fault primitives used by the nemesis.

// Partition blocks traffic in both directions between every node of a and
// every node of b. Calls nest: each must be undone by Heal with the same sides.
func (w *World) Partition(a, b []NodeID) {
	for _, x := range a {
		for _, y := range b {
			w.blocked[[2]NodeID{x, y}]++
			w.blocked[[2]NodeID{y, x}]++
		}
	}
}

// Heal undoes one Partition(a, b).
func (w *World) Heal(a, b []NodeID) {
	for _, x := range a {
		for _, y := range b {
			for _, k := range [][2]NodeID{{x, y}, {y, x}} {
				if w.blocked[k] > 0 {
					w.blocked[k]--
				}
			}
		}
	}
}

// Crash kills node id: volatile state, timers, in-flight disk writes and
// queued events are lost.
func (w *World) Crash(id NodeID) {
	if w.down[id] {
		return
	}
	w.down[id] = true
	w.epoch[id]++
	w.frozen[id] = false
	w.held[id] = nil
	w.disks[id] = &diskState{}
}

// Restart boots a crashed node.
func (w *World) Restart(id NodeID) {
	if !w.down[id] {
		return
	}
	w.down[id] = false
	w.nodes[id].Start()
}

// Down reports whether id is crashed.
func (w *World) Down(id NodeID) bool { return w.down[id] }

// Freeze suspends node id (SIGSTOP / cgroup freezer): it processes nothing,
// but messages and timers queue up.
func (w *World) Freeze(id NodeID) {
	if !w.down[id] {
		w.frozen[id] = true
	}
}

// Thaw resumes a frozen node; everything that queued up is delivered now, in
// the original order.
func (w *World) Thaw(id NodeID) {
	if !w.frozen[id] {
		return
	}
	w.frozen[id] = false
	held := w.held[id]
	w.held[id] = nil
	for _, e := range held {
		e.at = w.now
		w.push(e)
	}
}

// StallIO blocks fsync completion on node id.
func (w *World) StallIO(id NodeID) { w.stalled[id] = true }

// ResumeIO unblocks fsyncs on node id.
func (w *World) ResumeIO(id NodeID) {
	if !w.stalled[id] {
		return
	}
	w.stalled[id] = false
	w.kickDisk(id)
}

// SetLatency adds delay±jitter to every message to or from id.
func (w *World) SetLatency(id NodeID, delay, jitter time.Duration) {
	w.delay[id], w.jitter[id] = delay, jitter
}

// SetLoss drops messages to or from id with probability p.
func (w *World) SetLoss(id NodeID, p float64) { w.loss[id] = p }

// JumpClock steps node id's local clock by delta (an NTP step, a VM
// migration, an operator running date -s). Timers are relative and keep
// firing on schedule; only LocalNow readings jump.
func (w *World) JumpClock(id NodeID, delta time.Duration) {
	if c := w.clocks[id]; c != nil {
		c.localBase += int64(delta)
	}
}

// SetClockRate makes node id's local clock run at rate × real time.
func (w *World) SetClockRate(id NodeID, rate float64) {
	c := w.clocks[id]
	if c == nil || rate <= 0 {
		return
	}
	c.localBase = w.LocalNow(id)
	c.realBase = w.now
	c.rate = rate
}
