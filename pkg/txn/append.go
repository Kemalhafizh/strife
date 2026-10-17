package txn

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// Anomalies specific to list-append histories, after Elle.
const (
	// AnomalyGSingle is a cycle with exactly one anti-dependency (read
	// skew); snapshot isolation forbids it.
	AnomalyGSingle AnomalyType = "G-single"
	// AnomalyIncompatibleOrder: two reads of the same key returned lists
	// that are not prefixes of one another, so there is no single version
	// order (e.g. diverged replicas or a lost append).
	AnomalyIncompatibleOrder AnomalyType = "incompatible-order"
	// AnomalyPhantom: a read returned an element nobody ever appended.
	AnomalyPhantom AnomalyType = "phantom-read"
	// AnomalyDuplicate: a read returned the same element twice.
	AnomalyDuplicate AnomalyType = "duplicate-elements"
	// AnomalyInternal: a transaction did not observe its own earlier
	// appends or reads (internal inconsistency).
	AnomalyInternal AnomalyType = "internal"
)

// CheckAppend checks a history of list-append transactions against an
// isolation level, the way Elle does: every append writes a unique value, so
// a read of a key returns that key's entire version history, which yields
// the version order directly instead of guessing it. From it come
// write-write, write-read and read-write dependencies, plus real-time edges
// for strict serializability; a cycle the level forbids is a proof of
// violation.
//
// The check is sound (every anomaly is real, given unique appends) but not
// complete: anomalies involving appends that no read observed cannot be seen.
// Only operations with F == "txn" are considered.
func CheckAppend(ops []history.Operation, level IsolationLevel) *Report {
	if level == "" {
		level = LevelSerializable
	}
	a := analyse(ops)
	rep := &Report{Verdict: Pass, Level: level, Transactions: len(a.txns), Operations: a.microOps}
	anomalies := append([]Anomaly(nil), a.always...)
	if level != LevelReadUncommitted {
		anomalies = append(anomalies, a.readCommitted...)
	}
	g := a.graph(level == LevelStrictSerializable)
	rep.Edges = g.edges
	anomalies = append(anomalies, g.cycles(a, level)...)
	if len(anomalies) > 0 {
		rep.Verdict = Violation
		rep.Anomalies = anomalies
	}
	return rep
}

type elem struct {
	key string
	val int64
}

type atxn struct {
	op        history.Operation
	micro     []history.MicroOp
	committed bool // ok, or info with an observed append
}

type observation struct {
	txn  int
	list []int64
}

type analysis struct {
	txns     []atxn
	microOps int
	writer   map[elem]int // committed or indeterminate appends
	aborted  map[elem]int // appends of failed transactions
	dup      map[elem]bool
	own      []map[string][]int64 // per txn: its appends per key, in order
	reads    map[string][]observation
	order    map[string][]int64 // version order per key (longest read)
	pos      map[elem]int
	broken   map[string]bool // keys without a consistent version order
	// always lists anomalies forbidden at every level; readCommitted those
	// forbidden from read committed up (G1a, G1b).
	always, readCommitted []Anomaly
	// extRead is, per txn and read micro-op, the externally visible part
	// of the list it read (its own appends removed).
	extReads [][]extRead
}

type extRead struct {
	key string
	ext []int64
}

func (a *analysis) name(t int) string {
	return fmt.Sprintf("T%d", a.txns[t].op.ID)
}

func (a *analysis) txn(t int) Txn {
	o := a.txns[t].op
	tx := Txn{ID: o.ID, Process: o.Process, Status: o.Status(), Call: o.Call, Return: o.Return}
	return tx
}

func (a *analysis) describe(t int) string {
	return a.txns[t].op.Describe()
}

func analyse(ops []history.Operation) *analysis {
	a := &analysis{
		writer: map[elem]int{}, aborted: map[elem]int{}, dup: map[elem]bool{},
		reads: map[string][]observation{}, order: map[string][]int64{}, pos: map[elem]int{}, broken: map[string]bool{},
	}
	for _, o := range ops {
		if o.Input.F == history.FTxn {
			a.txns = append(a.txns, atxn{op: o, micro: o.Micro(), committed: o.Status() == history.OK})
		}
	}
	a.own = make([]map[string][]int64, len(a.txns))
	a.extReads = make([][]extRead, len(a.txns))

	// Who appended what.
	for t, tx := range a.txns {
		a.own[t] = map[string][]int64{}
		a.microOps += len(tx.micro)
		for _, m := range tx.micro {
			if m.F != history.MicroAppend || m.Value == nil {
				continue
			}
			e := elem{m.Key, *m.Value}
			a.own[t][m.Key] = append(a.own[t][m.Key], e.val)
			if tx.op.Status() == history.Fail {
				a.aborted[e] = t
				continue
			}
			if _, seen := a.writer[e]; seen {
				a.dup[e] = true // not unique: no inference may rely on it
			}
			a.writer[e] = t
		}
	}

	// What every committed transaction read.
	for t, tx := range a.txns {
		if tx.op.Status() != history.OK {
			continue
		}
		mine := map[string][]int64{}     // appends so far in this txn
		lastRead := map[string][]int64{} // most recent read per key
		readSince := map[string]bool{}
		for _, m := range tx.micro {
			switch m.F {
			case history.MicroAppend:
				if m.Value != nil {
					mine[m.Key] = append(mine[m.Key], *m.Value)
					if readSince[m.Key] {
						lastRead[m.Key] = append(lastRead[m.Key], *m.Value)
					}
				}
			case history.MicroRead:
				list := m.Values
				a.checkRead(t, m.Key, list, mine[m.Key], lastRead[m.Key], readSince[m.Key])
				lastRead[m.Key], readSince[m.Key] = append([]int64(nil), list...), true
				ext := list
				if own := mine[m.Key]; len(own) > 0 && len(list) >= len(own) && slices.Equal(list[len(list)-len(own):], own) {
					ext = list[:len(list)-len(own)]
				}
				a.extReads[t] = append(a.extReads[t], extRead{m.Key, ext})
				a.reads[m.Key] = append(a.reads[m.Key], observation{t, list})
				for _, v := range list {
					if w, ok := a.writer[elem{m.Key, v}]; ok {
						a.txns[w].committed = true // an observed append committed
					}
				}
			}
		}
	}

	// One version order per key: every read must be a prefix of the longest.
	keys := make([]string, 0, len(a.reads))
	for k := range a.reads {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		obs := a.reads[k]
		longest := obs[0]
		for _, o := range obs[1:] {
			if len(o.list) > len(longest.list) {
				longest = o
			}
		}
		for _, o := range obs {
			if !isPrefix(o.list, longest.list) {
				a.broken[k] = true
				a.always = append(a.always, Anomaly{
					Type: AnomalyIncompatibleOrder, Name: "Incompatible version order",
					Explanation: fmt.Sprintf("%s read %s = %v but %s read %s = %v: neither is a prefix of the other, so no single order of appends explains both (diverged replicas or a lost append).",
						a.name(o.txn), k, o.list, a.name(longest.txn), k, longest.list),
					Txns: []Txn{a.txn(o.txn), a.txn(longest.txn)},
				})
				break
			}
		}
		a.order[k] = longest.list
		for i, v := range longest.list {
			a.pos[elem{k, v}] = i
		}
	}
	return a
}

// checkRead records anomalies a single read proves on its own.
func (a *analysis) checkRead(t int, key string, list, mine, lastRead []int64, readBefore bool) {
	seen := map[int64]bool{}
	for _, v := range list {
		if seen[v] {
			a.always = append(a.always, Anomaly{
				Type: AnomalyDuplicate, Name: "Duplicate elements",
				Explanation: fmt.Sprintf("%s read %s = %v, which contains %d twice, but every value is appended once.", a.name(t), key, list, v),
				Txns:        []Txn{a.txn(t)},
			})
			break
		}
		seen[v] = true
	}
	for _, v := range list {
		e := elem{key, v}
		if _, ok := a.writer[e]; ok {
			continue
		}
		if w, ok := a.aborted[e]; ok {
			a.readCommitted = append(a.readCommitted, Anomaly{
				Type: AnomalyG1a, Name: "Aborted Read (G1a)",
				Explanation: fmt.Sprintf("%s read %s = %v, including %d, which was appended by %s; but %s failed (definitely aborted).",
					a.name(t), key, list, v, a.name(w), a.name(w)),
				Txns: []Txn{a.txn(w), a.txn(t)},
			})
		} else {
			a.always = append(a.always, Anomaly{
				Type: AnomalyPhantom, Name: "Phantom read",
				Explanation: fmt.Sprintf("%s read %s = %v, including %d, which no transaction ever appended to %s.", a.name(t), key, list, v, key),
				Txns:        []Txn{a.txn(t)},
			})
		}
		break
	}
	// Internal consistency: a transaction sees its own effects.
	want := mine
	if readBefore {
		want = lastRead
	}
	ok := len(list) >= len(want) && slices.Equal(list[len(list)-len(want):], want)
	if readBefore {
		ok = slices.Equal(list, lastRead)
	}
	if !ok {
		what := fmt.Sprintf("its own appends %v", want)
		if readBefore {
			what = fmt.Sprintf("%v (what it read before, plus its own appends since)", want)
		}
		a.always = append(a.always, Anomaly{
			Type: AnomalyInternal, Name: "Internal inconsistency",
			Explanation: fmt.Sprintf("%s read %s = %v, but within the same transaction it must observe %s.", a.name(t), key, list, what),
			Txns:        []Txn{a.txn(t)},
		})
	}
	// Intermediate read (G1b): the last foreign element is not the writer's
	// final append to this key.
	ext := list
	if len(mine) > 0 && len(list) >= len(mine) && slices.Equal(list[len(list)-len(mine):], mine) {
		ext = list[:len(list)-len(mine)]
	}
	if len(ext) == 0 {
		return
	}
	last := elem{key, ext[len(ext)-1]}
	w, ok := a.writer[last]
	if !ok || w == t || a.dup[last] {
		return
	}
	appends := a.own[w][key]
	if appends[len(appends)-1] != last.val {
		a.readCommitted = append(a.readCommitted, Anomaly{
			Type: AnomalyG1b, Name: "Intermediate Read (G1b)",
			Explanation: fmt.Sprintf("%s read %s = %v, ending in %d; but %s appended %v to %s, so %s observed %s in the middle of the transaction.",
				a.name(t), key, list, last.val, a.name(w), appends, key, a.name(t), a.name(w)),
			Txns: []Txn{a.txn(w), a.txn(t)},
		})
	}
}

func isPrefix(p, l []int64) bool { return len(p) <= len(l) && slices.Equal(p, l[:len(p)]) }

// ---------------------------------------------------------------------------
// Dependency graph.

type kind uint8

const (
	kWW kind = 1 << iota
	kWR
	kRW
	kRT
)

func (k kind) edgeKind() EdgeKind {
	switch k {
	case kWW:
		return EdgeWW
	case kWR:
		return EdgeWR
	case kRW:
		return EdgeRW
	}
	return EdgeRO
}

type gEdge struct {
	to   int
	kind kind
	key  string
	desc string
}

type depGraph struct {
	adj   [][]gEdge
	edges int
}

func (g *depGraph) add(from, to int, k kind, key, desc string) {
	if from == to {
		return
	}
	for _, e := range g.adj[from] {
		if e.to == to && e.kind == k {
			return
		}
	}
	g.adj[from] = append(g.adj[from], gEdge{to, k, key, desc})
	g.edges++
}

func (a *analysis) graph(realtime bool) *depGraph {
	g := &depGraph{adj: make([][]gEdge, len(a.txns))}
	usable := func(e elem) (int, bool) {
		w, ok := a.writer[e]
		return w, ok && !a.dup[e] && a.txns[w].committed
	}
	keys := make([]string, 0, len(a.order))
	for k := range a.order {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if a.broken[k] {
			continue
		}
		order := a.order[k]
		for i := 0; i+1 < len(order); i++ {
			w1, ok1 := usable(elem{k, order[i]})
			w2, ok2 := usable(elem{k, order[i+1]})
			if ok1 && ok2 {
				g.add(w1, w2, kWW, k, fmt.Sprintf("%s appended %d to %s, and %s appended %d right after it", a.name(w1), order[i], k, a.name(w2), order[i+1]))
			}
		}
	}
	// Committed appends no read observed come after every observed element:
	// the longest read is a prefix of the key's final list, which contains
	// them. Their exact order is unknown, so they get edges only from what
	// provably precedes them.
	unseen := map[string][]elem{}
	for e, w := range a.writer {
		if _, observed := a.pos[e]; !observed && !a.dup[e] && a.txns[w].op.Status() == history.OK && !a.broken[e.key] {
			unseen[e.key] = append(unseen[e.key], e)
		}
	}
	for _, k := range keys {
		es := unseen[k]
		sort.Slice(es, func(i, j int) bool { return es[i].val < es[j].val })
		order := a.order[k]
		if len(order) == 0 {
			continue
		}
		last := elem{k, order[len(order)-1]}
		w1, ok := usable(last)
		if !ok {
			continue
		}
		for _, e := range es {
			w2 := a.writer[e]
			g.add(w1, w2, kWW, k, fmt.Sprintf("%s appended %d to %s, the last element any read observed, so %s's append of %d (committed, never observed) came after it", a.name(w1), last.val, k, a.name(w2), e.val))
		}
	}
	for t, reads := range a.extReads {
		for _, r := range reads {
			if a.broken[r.key] {
				continue
			}
			order := a.order[r.key]
			next := 0 // position of the first version r did not see
			if n := len(r.ext); n > 0 {
				last := elem{r.key, r.ext[n-1]}
				w, ok := usable(last)
				if !ok {
					continue
				}
				g.add(w, t, kWR, r.key, fmt.Sprintf("%s read %s = %v, observing %s's append of %d", a.name(t), r.key, r.ext, a.name(w), last.val))
				next = a.pos[last] + 1
			}
			if next == len(order) {
				// r saw the longest list: every unobserved committed append
				// to the key is a later version r missed.
				for _, e := range unseen[r.key] {
					w := a.writer[e]
					if w == t {
						continue
					}
					seen := "nothing"
					if len(r.ext) > 0 {
						seen = fmt.Sprint(r.ext)
					}
					g.add(t, w, kRW, r.key, fmt.Sprintf("%s read %s = %s, missing %d, which %s appended (committed) later", a.name(t), r.key, seen, e.val, a.name(w)))
				}
			}
			if next < len(order) {
				nx := elem{r.key, order[next]}
				if w, ok := usable(nx); ok {
					seen := "nothing"
					if len(r.ext) > 0 {
						seen = fmt.Sprint(r.ext)
					}
					g.add(t, w, kRW, r.key, fmt.Sprintf("%s read %s = %s, missing %d, which %s appended next", a.name(t), r.key, seen, nx.val, a.name(w)))
				}
			}
		}
	}
	if realtime {
		a.realtime(g)
	}
	return g
}

// realtime adds, for every completed transaction, edges to the transactions
// that began after it completed. Only the frontier is added (b began before
// any transaction that began after a had itself completed); every other
// real-time successor is reachable through it, so cycles are preserved
// with O(n·concurrency) edges instead of O(n²).
func (a *analysis) realtime(g *depGraph) {
	var targets []int
	for t, tx := range a.txns {
		if tx.committed {
			targets = append(targets, t)
		}
	}
	sort.Slice(targets, func(i, j int) bool { return a.txns[targets[i]].op.Call < a.txns[targets[j]].op.Call })
	suffixMin := make([]int64, len(targets)+1)
	suffixMin[len(targets)] = history.Infinity
	for i := len(targets) - 1; i >= 0; i-- {
		suffixMin[i] = min(suffixMin[i+1], a.txns[targets[i]].op.Return)
	}
	for s, tx := range a.txns {
		if !tx.committed || tx.op.Return == history.Infinity {
			continue
		}
		ret := tx.op.Return
		j := sort.Search(len(targets), func(i int) bool { return a.txns[targets[i]].op.Call > ret })
		m := suffixMin[j]
		for ; j < len(targets) && a.txns[targets[j]].op.Call <= m; j++ {
			t := targets[j]
			g.add(s, t, kRT, "", fmt.Sprintf("%s completed at %v, before %s began at %v", a.name(s), ns(ret), a.name(t), ns(a.txns[t].op.Call)))
		}
	}
}

func ns(t int64) time.Duration { return time.Duration(t).Round(time.Microsecond) }

// sccs returns the strongly connected components (as component ids per
// node) of the subgraph with edges of the given kinds, iteratively so that
// long dependency chains cannot overflow the stack.
func (g *depGraph) sccs(mask kind) []int {
	n := len(g.adj)
	index := make([]int, n)
	low := make([]int, n)
	comp := make([]int, n)
	onStack := make([]bool, n)
	for i := range index {
		index[i], comp[i] = -1, -1
	}
	var stack []int
	next, ncomp := 0, 0
	type frame struct{ v, e int }
	for root := 0; root < n; root++ {
		if index[root] >= 0 {
			continue
		}
		call := []frame{{root, 0}}
		index[root], low[root] = next, next
		next++
		stack = append(stack, root)
		onStack[root] = true
		for len(call) > 0 {
			f := &call[len(call)-1]
			v := f.v
			if f.e < len(g.adj[v]) {
				e := g.adj[v][f.e]
				f.e++
				if e.kind&mask == 0 {
					continue
				}
				w := e.to
				if index[w] < 0 {
					index[w], low[w] = next, next
					next++
					stack = append(stack, w)
					onStack[w] = true
					call = append(call, frame{w, 0})
				} else if onStack[w] {
					low[v] = min(low[v], index[w])
				}
				continue
			}
			call = call[:len(call)-1]
			if len(call) > 0 {
				p := call[len(call)-1].v
				low[p] = min(low[p], low[v])
			}
			if low[v] == index[v] {
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					onStack[w] = false
					comp[w] = ncomp
					if w == v {
						break
					}
				}
				ncomp++
			}
		}
	}
	return comp
}

type step struct {
	from int
	e    gEdge
}

type path []step

// bfs finds a shortest path from src to a different node dst using edges of
// mask whose endpoints lie in component c.
func (g *depGraph) bfs(src, dst int, mask kind, comp []int, c int) path {
	back := map[int]step{}
	visited := map[int]bool{src: true}
	queue := []int{src}
	for len(queue) > 0 && !visited[dst] {
		v := queue[0]
		queue = queue[1:]
		for _, e := range g.adj[v] {
			if e.kind&mask == 0 || comp[e.to] != c || visited[e.to] {
				continue
			}
			visited[e.to] = true
			back[e.to] = step{v, e}
			if e.to == dst {
				break
			}
			queue = append(queue, e.to)
		}
	}
	if !visited[dst] {
		return nil
	}
	var out path
	for v := dst; v != src; v = back[v].from {
		out = append(out, back[v])
	}
	slices.Reverse(out)
	return out
}

// cycleThrough returns a shortest cycle that starts with edge u→e.to and
// returns to u along edges of mask, within component c.
func (g *depGraph) cycleThrough(u int, e gEdge, mask kind, comp []int, c int) path {
	rest := g.bfs(e.to, u, mask, comp, c)
	if rest == nil {
		return nil
	}
	return append(path{{u, e}}, rest...)
}

// maxAnomalyCycles bounds how many cyclic components are explained.
const maxAnomalyCycles = 16

// cycles finds, per cyclic component, the weakest anomaly the level forbids.
func (g *depGraph) cycles(a *analysis, level IsolationLevel) []Anomaly {
	type spec struct {
		typ   AnomalyType
		name  string
		pivot kind // edge kind the cycle must start with
		mask  kind // edge kinds allowed in the rest of the cycle
		why   string
	}
	all := []spec{
		{AnomalyG0, "Dirty Write (G0)", kWW, kWW, "a cycle of write-write dependencies: no order of the appends agrees with every observed version order."},
		{AnomalyG1c, "Circular Information Flow (G1c)", kWR, kWW | kWR, "a cycle of write-write and write-read dependencies: each transaction observed the other's effects."},
		{AnomalyGSingle, "Read Skew (G-single)", kRW, kWW | kWR, "a cycle with exactly one anti-dependency: a transaction missed a write that it also, transitively, depends on."},
		{AnomalyG2, "Anti-dependency Cycle (G2)", kRW, kWW | kWR | kRW, "a cycle with anti-dependencies (e.g. write skew): no serial order of these transactions produces what they read."},
		{AnomalyGRealtime, "Real-time Violation (G-realtime)", kRT, kWW | kWR | kRW | kRT, "a cycle through real-time order: the database's order contradicts the order in which the transactions actually ran."},
	}
	var forbidden []spec
	for _, s := range all {
		switch s.typ {
		case AnomalyG0:
			forbidden = append(forbidden, s)
		case AnomalyG1c:
			if level != LevelReadUncommitted {
				forbidden = append(forbidden, s)
			}
		case AnomalyGSingle:
			if level == LevelSnapshotIsolation || level == LevelRepeatableRead || level == LevelSerializable || level == LevelStrictSerializable {
				forbidden = append(forbidden, s)
			}
		case AnomalyG2:
			if level == LevelRepeatableRead || level == LevelSerializable || level == LevelStrictSerializable {
				forbidden = append(forbidden, s)
			}
		case AnomalyGRealtime:
			if level == LevelStrictSerializable {
				forbidden = append(forbidden, s)
			}
		}
	}
	var union kind
	for _, s := range forbidden {
		union |= s.pivot | s.mask
	}
	comp := g.sccs(union)
	members := map[int][]int{}
	for v, c := range comp {
		members[c] = append(members[c], v)
	}
	ids := make([]int, 0, len(members))
	for c, m := range members {
		if len(m) > 1 {
			ids = append(ids, c)
		}
	}
	// Report components in history order, so the output is deterministic.
	sort.Slice(ids, func(i, j int) bool { return members[ids[i]][0] < members[ids[j]][0] })
	var out []Anomaly
	for _, c := range ids {
		if len(out) >= maxAnomalyCycles {
			break
		}
		// The weakest forbidden anomaly in the component. Trying every
		// pivot edge of a huge component would be quadratic, so attempts
		// are capped: a cycle found is always real, a miss only means the
		// component is reported under a stronger anomaly.
		for _, s := range forbidden {
			var cyc path
			attempts := 0
			for _, u := range members[c] {
				for _, e := range g.adj[u] {
					if e.kind != s.pivot || comp[e.to] != c {
						continue
					}
					if attempts++; attempts > maxPivotAttempts {
						break
					}
					if cyc = g.cycleThrough(u, e, s.mask, comp, c); cyc != nil {
						break
					}
				}
				if cyc != nil || attempts > maxPivotAttempts {
					break
				}
			}
			if cyc != nil {
				out = append(out, a.cycleAnomaly(s.typ, s.name, s.why, cyc))
				break
			}
		}
	}
	return out
}

// maxPivotAttempts bounds the cycle searches per anomaly and component.
const maxPivotAttempts = 64

func (a *analysis) cycleAnomaly(typ AnomalyType, name, why string, p path) Anomaly {
	c := &Cycle{}
	var steps []string
	var txns []Txn
	for i, s := range p {
		from, to := a.txns[s.from].op.ID, a.txns[s.e.to].op.ID
		if i == 0 {
			c.Nodes = append(c.Nodes, from)
		}
		c.Nodes = append(c.Nodes, to)
		c.Edges = append(c.Edges, Edge{From: from, To: to, Kind: s.e.kind.edgeKind(), Key: s.e.key, Desc: s.e.desc})
		steps = append(steps, s.e.desc)
		txns = append(txns, a.txn(s.from))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s", name, why)
	for i, s := range steps {
		fmt.Fprintf(&b, " (%d) %s.", i+1, s)
	}
	b.WriteString(" Transactions:")
	for _, s := range p {
		fmt.Fprintf(&b, " %s = %s;", a.name(s.from), a.describe(s.from))
	}
	return Anomaly{Type: typ, Name: name, Explanation: strings.TrimSuffix(b.String(), ";") + ".", Cycle: c, Txns: txns}
}
