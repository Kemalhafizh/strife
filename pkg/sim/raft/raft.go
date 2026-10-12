// Package raft is a compact Raft implementation that runs inside the Strife
// simulator. It is the reference "system under test": the correct
// configuration must survive every fault schedule, and each Bug knob
// re-creates a class of real-world consensus bugs that Strife must catch.
//
// Implemented: leader election with randomized timeouts, log replication with
// fast conflict backtracking, commitment restricted to current-term entries,
// a no-op entry on election, durable term/vote/log through simulated fsync,
// and linearizable reads through the log (or leases, when enabled).
// Not implemented: snapshots, membership changes, client deduplication.
package raft

import (
	"sort"
	"time"

	"github.com/kemalhafizh/strife/internal/rng"
	"github.com/kemalhafizh/strife/pkg/sim"
)

// Config tunes a node.
type Config struct {
	ElectionMin time.Duration
	ElectionMax time.Duration
	Heartbeat   time.Duration
	MaxBatch    int

	// StaleReads serves reads from the leader's local state without
	// confirming leadership: a deposed leader returns stale data.
	StaleReads bool
	// LeaseReads serves reads locally while a leader lease, measured on the
	// node's wall clock, is valid. Correct only while clocks behave: a
	// backwards jump on the leader (or a forward jump / fast drift on a
	// majority of followers) lets a new leader commit while the old one still
	// believes its lease holds.
	LeaseReads bool
	// UnsafeFsync acknowledges votes and entries before they are durable
	// (think fsync=off or a lying disk cache). A crash loses acked state.
	UnsafeFsync bool
}

// Defaults returns the timing parameters used by Strife's built-in systems.
func Defaults() Config {
	return Config{
		ElectionMin: 150 * time.Millisecond,
		ElectionMax: 300 * time.Millisecond,
		Heartbeat:   50 * time.Millisecond,
		MaxBatch:    64,
	}
}

// Entry is one log entry.
type Entry struct {
	Term   uint64
	Noop   bool
	Op     sim.KVOp
	Client sim.NodeID
	ReqID  uint64
}

// RequestVote is sent by candidates.
type RequestVote struct {
	Term      uint64
	Candidate sim.NodeID
	LastIndex int
	LastTerm  uint64
}

// Vote answers RequestVote.
type Vote struct {
	Term    uint64
	ReqTerm uint64
	Granted bool
}

// AppendEntries replicates entries and doubles as the heartbeat.
type AppendEntries struct {
	Term      uint64
	Leader    sim.NodeID
	PrevIndex int
	PrevTerm  uint64
	Entries   []Entry
	Commit    int
	Round     uint64
}

// AppendReply answers AppendEntries.
type AppendReply struct {
	Term     uint64
	ReqTerm  uint64
	Round    uint64
	Success  bool
	Match    int
	Conflict int
}

type role uint8

const (
	follower role = iota
	candidate
	leader
)

type durable struct {
	term     uint64
	votedFor sim.NodeID
	log      []Entry
}

type pending struct {
	client sim.NodeID
	reqID  uint64
	term   uint64
}

// Node is a Raft server.
type Node struct {
	w     *sim.World
	id    sim.NodeID
	n     int
	cfg   Config
	rand  *rng.Rand
	disk  durable
	state durable

	role    role
	leader  sim.NodeID
	commit  int
	applied int
	kv      map[string]int64
	pending map[int]pending

	votes       []bool
	next, match []int

	electionGen uint64
	hbGen       uint64
	lastHeard   int64
	bootAt      int64

	// Lease bookkeeping (leader only).
	round      uint64
	roundStart []int64
	acked      []uint64
	noopIndex  int
}

// New creates node id of an n-node cluster.
func New(w *sim.World, id sim.NodeID, n int, cfg Config, r *rng.Rand) *Node {
	return &Node{
		w: w, id: id, n: n, cfg: cfg, rand: r,
		disk: durable{votedFor: -1, log: []Entry{{}}},
	}
}

// Start implements sim.Node: boot from durable state only.
func (n *Node) Start() {
	n.state = durable{term: n.disk.term, votedFor: n.disk.votedFor, log: append([]Entry(nil), n.disk.log...)}
	n.role = follower
	n.leader = -1
	n.commit, n.applied = 0, 0
	n.kv = map[string]int64{}
	n.pending = map[int]pending{}
	n.hbGen++
	n.bootAt = n.w.LocalNow(n.id)
	n.lastHeard = -1 << 62
	n.resetElection()
}

func (n *Node) lastIndex() int            { return len(n.state.log) - 1 }
func (n *Node) lastTerm() uint64          { return n.state.log[n.lastIndex()].Term }
func (n *Node) majority() int             { return n.n/2 + 1 }
func (n *Node) send(to sim.NodeID, m any) { n.w.Send(n.id, to, m) }

// persist makes the current term, vote and log durable, then runs then.
func (n *Node) persist(then func()) {
	snap := n.state
	snap.log = snap.log[:len(snap.log):len(snap.log)]
	n.w.Fsync(n.id, func() {
		n.disk = snap
		if !n.cfg.UnsafeFsync && then != nil {
			then()
		}
	})
	if n.cfg.UnsafeFsync && then != nil {
		then()
	}
}

func (n *Node) resetElection() {
	n.electionGen++
	gen := n.electionGen
	d := time.Duration(n.rand.Range(int64(n.cfg.ElectionMin), int64(n.cfg.ElectionMax)))
	n.w.After(n.id, d, func() {
		if gen == n.electionGen && n.role != leader {
			n.startElection()
		}
	})
}

func (n *Node) stepDown(term uint64) {
	if term > n.state.term {
		n.state.term = term
		n.state.votedFor = -1
		n.leader = -1
	}
	if n.role == leader {
		n.hbGen++
	}
	n.role = follower
	n.pending = map[int]pending{}
	n.resetElection()
}

func (n *Node) startElection() {
	n.state.term++
	n.state.votedFor = n.id
	n.role = candidate
	n.leader = -1
	n.votes = make([]bool, n.n)
	n.votes[n.id] = true
	term := n.state.term
	req := RequestVote{Term: term, Candidate: n.id, LastIndex: n.lastIndex(), LastTerm: n.lastTerm()}
	n.persist(func() {
		if n.role != candidate || n.state.term != term {
			return
		}
		for p := 0; p < n.n; p++ {
			if sim.NodeID(p) != n.id {
				n.send(sim.NodeID(p), req)
			}
		}
		n.maybeWin()
	})
	n.resetElection()
}

func (n *Node) maybeWin() {
	c := 0
	for _, v := range n.votes {
		if v {
			c++
		}
	}
	if c >= n.majority() {
		n.becomeLeader()
	}
}

func (n *Node) becomeLeader() {
	n.role = leader
	n.leader = n.id
	n.electionGen++
	n.next = make([]int, n.n)
	n.match = make([]int, n.n)
	for i := range n.next {
		n.next[i] = n.lastIndex() + 1
	}
	n.round = 1
	n.roundStart = []int64{0, n.w.LocalNow(n.id)}
	n.acked = make([]uint64, n.n)
	n.state.log = append(n.state.log, Entry{Term: n.state.term, Noop: true})
	n.noopIndex = n.lastIndex()
	n.leaderAppended(n.noopIndex)
	n.broadcast()
	n.hbGen++
	n.heartbeat(n.hbGen)
}

func (n *Node) heartbeat(gen uint64) {
	n.w.After(n.id, n.cfg.Heartbeat, func() {
		if gen != n.hbGen || n.role != leader {
			return
		}
		n.round++
		n.roundStart = append(n.roundStart, n.w.LocalNow(n.id))
		n.broadcast()
		n.heartbeat(gen)
	})
}

// leaderAppended persists the leader's own log and counts it towards the
// commit quorum only once durable.
func (n *Node) leaderAppended(idx int) {
	term := n.state.term
	n.persist(func() {
		if n.role == leader && n.state.term == term && idx > n.match[n.id] {
			n.match[n.id] = idx
			n.advanceCommit()
		}
	})
}

func (n *Node) broadcast() {
	for p := 0; p < n.n; p++ {
		if sim.NodeID(p) != n.id {
			n.sendAppend(sim.NodeID(p))
		}
	}
}

func (n *Node) sendAppend(p sim.NodeID) {
	prev := n.next[p] - 1
	if prev > n.lastIndex() {
		prev = n.lastIndex()
	}
	end := prev + 1 + n.cfg.MaxBatch
	if end > len(n.state.log) {
		end = len(n.state.log)
	}
	n.send(p, AppendEntries{
		Term:      n.state.term,
		Leader:    n.id,
		PrevIndex: prev,
		PrevTerm:  n.state.log[prev].Term,
		Entries:   append([]Entry(nil), n.state.log[prev+1:end]...),
		Commit:    n.commit,
		Round:     n.round,
	})
}

func (n *Node) advanceCommit() {
	for idx := n.lastIndex(); idx > n.commit; idx-- {
		if n.state.log[idx].Term != n.state.term {
			break
		}
		c := 0
		for _, m := range n.match {
			if m >= idx {
				c++
			}
		}
		if c >= n.majority() {
			n.commit = idx
			n.applyCommitted()
			return
		}
	}
}

func (n *Node) applyCommitted() {
	for n.applied < n.commit {
		n.applied++
		e := n.state.log[n.applied]
		reply := n.apply(e)
		if p, ok := n.pending[n.applied]; ok {
			delete(n.pending, n.applied)
			if n.role == leader && p.term == e.Term && p.client == e.Client && p.reqID == e.ReqID {
				n.send(p.client, reply)
			}
		}
	}
}

func (n *Node) apply(e Entry) sim.ClientReply {
	r := sim.ClientReply{ReqID: e.ReqID, Status: sim.StatusOK, Leader: n.id}
	if e.Noop {
		return r
	}
	switch e.Op.F {
	case "read":
		r.Value, r.Found = n.kv[e.Op.Key]
	case "write":
		n.kv[e.Op.Key] = e.Op.Value
	case "cas":
		if v, ok := n.kv[e.Op.Key]; ok && v == e.Op.Old {
			n.kv[e.Op.Key] = e.Op.Value
		} else {
			r.Status = sim.StatusFail
		}
	}
	return r
}

// heardRecently implements the leader stickiness rule required by leases: a
// server that heard from a live leader (or just booted) within the minimum
// election timeout refuses to help elect someone else.
func (n *Node) heardRecently() bool {
	now := n.w.LocalNow(n.id)
	if n.role == leader {
		return true
	}
	if now-n.bootAt < int64(n.cfg.ElectionMin) {
		return true
	}
	return n.leader != -1 && now-n.lastHeard < int64(n.cfg.ElectionMin)
}

func (n *Node) leaseValid() bool {
	if n.role != leader {
		return false
	}
	rounds := make([]uint64, 0, n.n)
	for p := 0; p < n.n; p++ {
		if sim.NodeID(p) == n.id {
			rounds = append(rounds, n.round)
		} else {
			rounds = append(rounds, n.acked[p])
		}
	}
	sort.Slice(rounds, func(i, j int) bool { return rounds[i] > rounds[j] })
	r := rounds[n.majority()-1]
	if r == 0 {
		return false
	}
	lease := int64(n.cfg.ElectionMin) * 9 / 10
	return n.w.LocalNow(n.id) < n.roundStart[r]+lease
}

// Receive implements sim.Node.
func (n *Node) Receive(from sim.NodeID, msg any) {
	switch m := msg.(type) {
	case sim.ClientRequest:
		n.onClient(from, m)
	case RequestVote:
		n.onRequestVote(from, m)
	case Vote:
		n.onVote(from, m)
	case AppendEntries:
		n.onAppend(from, m)
	case AppendReply:
		n.onAppendReply(from, m)
	}
}

func (n *Node) onClient(from sim.NodeID, m sim.ClientRequest) {
	if n.role != leader {
		n.send(from, sim.ClientReply{ReqID: m.ReqID, Status: sim.StatusRedirect, Leader: n.leader})
		return
	}
	if m.Op.F == "read" && (n.cfg.StaleReads || (n.cfg.LeaseReads && n.applied >= n.noopIndex && n.leaseValid())) {
		r := sim.ClientReply{ReqID: m.ReqID, Status: sim.StatusOK, Leader: n.id}
		r.Value, r.Found = n.kv[m.Op.Key]
		n.send(from, r)
		return
	}
	n.state.log = append(n.state.log, Entry{Term: n.state.term, Op: m.Op, Client: from, ReqID: m.ReqID})
	idx := n.lastIndex()
	n.pending[idx] = pending{client: from, reqID: m.ReqID, term: n.state.term}
	n.leaderAppended(idx)
	n.broadcast()
}

func (n *Node) onRequestVote(from sim.NodeID, m RequestVote) {
	if n.cfg.LeaseReads && m.Term > n.state.term && n.heardRecently() {
		n.send(from, Vote{Term: n.state.term, ReqTerm: m.Term})
		return
	}
	if m.Term > n.state.term {
		n.stepDown(m.Term)
	}
	upToDate := m.LastTerm > n.lastTerm() || (m.LastTerm == n.lastTerm() && m.LastIndex >= n.lastIndex())
	grant := m.Term == n.state.term && (n.state.votedFor == -1 || n.state.votedFor == m.Candidate) && upToDate
	if grant {
		n.state.votedFor = m.Candidate
		n.resetElection()
	}
	reply := Vote{Term: n.state.term, ReqTerm: m.Term, Granted: grant}
	n.persist(func() { n.send(from, reply) })
}

func (n *Node) onVote(from sim.NodeID, m Vote) {
	if m.Term > n.state.term {
		n.stepDown(m.Term)
		n.persist(nil)
		return
	}
	if n.role == candidate && m.ReqTerm == n.state.term && m.Granted {
		n.votes[from] = true
		n.maybeWin()
	}
}

func (n *Node) onAppend(from sim.NodeID, m AppendEntries) {
	if m.Term < n.state.term {
		n.send(from, AppendReply{Term: n.state.term, ReqTerm: m.Term, Round: m.Round})
		return
	}
	if m.Term > n.state.term || n.role != follower {
		n.stepDown(m.Term)
	}
	n.leader = m.Leader
	n.lastHeard = n.w.LocalNow(n.id)
	n.resetElection()

	reply := AppendReply{Term: n.state.term, ReqTerm: m.Term, Round: m.Round}
	switch {
	case m.PrevIndex > n.lastIndex():
		reply.Conflict = n.lastIndex() + 1
	case n.state.log[m.PrevIndex].Term != m.PrevTerm:
		t := n.state.log[m.PrevIndex].Term
		i := m.PrevIndex
		for i > 1 && n.state.log[i-1].Term == t {
			i--
		}
		reply.Conflict = i
	default:
		for i, e := range m.Entries {
			idx := m.PrevIndex + 1 + i
			if idx <= n.lastIndex() {
				if n.state.log[idx].Term == e.Term {
					continue
				}
				// Truncation always reallocates so durable snapshots that
				// share the backing array are never overwritten.
				n.state.log = append(append([]Entry(nil), n.state.log[:idx]...), m.Entries[i:]...)
			} else {
				n.state.log = append(n.state.log, m.Entries[i:]...)
			}
			break
		}
		reply.Success = true
		reply.Match = m.PrevIndex + len(m.Entries)
		if c := min(m.Commit, reply.Match); c > n.commit {
			n.commit = c
			n.applyCommitted()
		}
	}
	n.persist(func() { n.send(from, reply) })
}

func (n *Node) onAppendReply(from sim.NodeID, m AppendReply) {
	if m.Term > n.state.term {
		n.stepDown(m.Term)
		n.persist(nil)
		return
	}
	if n.role != leader || m.ReqTerm != n.state.term {
		return
	}
	if m.Round > n.acked[from] {
		n.acked[from] = m.Round
	}
	if m.Success {
		if m.Match > n.match[from] {
			n.match[from] = m.Match
		}
		advanced := m.Match+1 > n.next[from]
		if advanced {
			n.next[from] = m.Match + 1
		}
		n.advanceCommit()
		// Keep a lagging follower streaming, but do not answer every
		// duplicate ack with another copy of the same entries.
		if advanced && n.next[from] <= n.lastIndex() {
			n.sendAppend(from)
		}
		return
	}
	if m.Conflict < n.next[from] {
		n.next[from] = max(1, m.Conflict)
		n.sendAppend(from)
	}
}
