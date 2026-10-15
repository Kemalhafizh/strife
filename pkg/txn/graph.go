package txn

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kemalhafizh/strife/pkg/history"
)

// EdgeKind classifies the relationship between two transactions.
type EdgeKind string

const (
	EdgeWR EdgeKind = "wr" // write-read dependency
	EdgeWW EdgeKind = "ww" // write-write dependency (version order)
	EdgeRW EdgeKind = "rw" // anti-dependency: read of older version vs writer of newer
	EdgeSO EdgeKind = "so" // session / process order
	EdgeRO EdgeKind = "ro" // real-time order (strict serializability)
)

// Edge represents a directed dependency from one transaction to another.
type Edge struct {
	From  int      `json:"from"`  // source Txn ID
	To    int      `json:"to"`    // target Txn ID
	Kind  EdgeKind `json:"kind"`  // wr, ww, rw, so, ro
	Key   string   `json:"key"`   // relevant key, or empty for so/ro
	Value *int64   `json:"value"` // relevant value, if applicable
	Desc  string   `json:"desc"`  // human-readable description
}

func (e Edge) String() string {
	if e.Desc != "" {
		return e.Desc
	}
	val := ""
	if e.Value != nil {
		val = fmt.Sprintf("=%d", *e.Value)
	}
	if e.Key != "" {
		return fmt.Sprintf("T%d ──%s(%s%s)──> T%d", e.From, e.Kind, e.Key, val, e.To)
	}
	return fmt.Sprintf("T%d ──%s──> T%d", e.From, e.Kind, e.To)
}

// Graph is the Direct Serialization Graph (DSG) over transactions.
type Graph struct {
	Txns  map[int]Txn      // Txn ID -> Txn
	Nodes []int            // sorted list of Txn IDs
	Adj   map[int][]Edge   // outgoing edges per Txn ID
	Edges []Edge           // all edges in the graph
}

// NewGraph initializes an empty DSG.
func NewGraph() *Graph {
	return &Graph{
		Txns: make(map[int]Txn),
		Adj:  make(map[int][]Edge),
	}
}

// AddTxn adds a transaction node to the graph.
func (g *Graph) AddTxn(t Txn) {
	if _, ok := g.Txns[t.ID]; !ok {
		g.Txns[t.ID] = t
		g.Nodes = append(g.Nodes, t.ID)
	}
}

// AddEdge adds a directed dependency edge if it does not already exist.
func (g *Graph) AddEdge(e Edge) {
	if e.From == e.To {
		// Self-loops within the same transaction are internal
		return
	}
	// Check for duplicate edge
	for _, existing := range g.Adj[e.From] {
		if existing.To == e.To && existing.Kind == e.Kind && existing.Key == e.Key {
			return
		}
	}
	g.Adj[e.From] = append(g.Adj[e.From], e)
	g.Edges = append(g.Edges, e)
}

// Subgraph returns a new Graph containing only edges of the specified kinds.
func (g *Graph) Subgraph(kinds ...EdgeKind) *Graph {
	sub := NewGraph()
	for _, id := range g.Nodes {
		sub.AddTxn(g.Txns[id])
	}
	allowed := make(map[EdgeKind]bool, len(kinds))
	for _, k := range kinds {
		allowed[k] = true
	}
	for _, e := range g.Edges {
		if allowed[e.Kind] {
			sub.AddEdge(e)
		}
	}
	return sub
}

// BuildGraph constructs the Direct Serialization Graph from a set of transactions.
// It infers:
//   - wr edges (when T2 reads a value written by T1);
//   - ww edges (version order between writers of the same key);
//   - rw edges (anti-dependencies: T1 read version V of key K, and T2 wrote a later version of K);
//   - so edges (session order within the same process);
//   - ro edges (real-time order where T1 completed before T2 started).
func BuildGraph(txns []Txn) *Graph {
	g := NewGraph()
	for _, t := range txns {
		g.AddTxn(t)
	}

	// 1. Index writers: writers[key][val] -> TxnID
	type writeEntry struct {
		txnID int
		val   int64
	}
	writesByKey := make(map[string][]writeEntry)
	writerOfVal := make(map[string]map[int64]int) // key -> val -> txnID

	for _, t := range txns {
		if t.Status == history.Fail {
			continue // Aborted txns are analyzed separately for G1a
		}
		for _, op := range t.Ops {
			if op.Kind == OpWrite && op.Value != nil {
				k := op.Key
				v := *op.Value
				if writerOfVal[k] == nil {
					writerOfVal[k] = make(map[int64]int)
				}
				writerOfVal[k][v] = t.ID
				writesByKey[k] = append(writesByKey[k], writeEntry{txnID: t.ID, val: v})
			}
		}
	}

	// 2. Infer WR (Write-Read) dependencies
	for _, reader := range txns {
		if reader.Status == history.Fail {
			continue
		}
		for _, op := range reader.Ops {
			if op.Kind == OpRead && op.Value != nil {
				k := op.Key
				v := *op.Value
				if writerID, ok := writerOfVal[k][v]; ok && writerID != reader.ID {
					g.AddEdge(Edge{
						From:  writerID,
						To:    reader.ID,
						Kind:  EdgeWR,
						Key:   k,
						Value: op.Value,
						Desc:  fmt.Sprintf("T%d ──wr(%s=%d)──> T%d", writerID, k, v, reader.ID),
					})
				}
			}
		}
	}

	// 3. Infer WW (Write-Write) version order per key.
	// We infer version order between writers w1 and w2 of key k by:
	// a) Real-time completion: w1 completed before w2 began (w1.Return < w2.Call).
	// b) Observed order: reader r1 read w1's write, reader r2 read w2's write,
	//    and r1 finished before r2 began (r1.Return < r2.Call) or r1 precedes r2 in session order.
	wwPairs := make(map[string]map[int]map[int]bool) // key -> w1 -> w2 -> true
	recordWW := func(key string, w1, w2 int) {
		if w1 == w2 {
			return
		}
		if wwPairs[key] == nil {
			wwPairs[key] = make(map[int]map[int]bool)
		}
		if wwPairs[key][w1] == nil {
			wwPairs[key][w1] = make(map[int]bool)
		}
		if !wwPairs[key][w1][w2] {
			wwPairs[key][w1][w2] = true
			g.AddEdge(Edge{
				From: w1,
				To:   w2,
				Kind: EdgeWW,
				Key:  key,
				Desc: fmt.Sprintf("T%d ──ww(%s)──> T%d", w1, key, w2),
			})
		}
	}

	for k, entries := range writesByKey {
		for i := 0; i < len(entries); i++ {
			for j := 0; j < len(entries); j++ {
				if i == j {
					continue
				}
				w1 := entries[i].txnID
				w2 := entries[j].txnID
				t1 := g.Txns[w1]
				t2 := g.Txns[w2]

				// If w1 completed before w2 began, w1 is earlier version
				if t1.Return != history.Infinity && t1.Return < t2.Call {
					recordWW(k, w1, w2)
				}
			}
		}
	}

	// Also infer WW from observed reader sequences:
	// If r1 read k=v1 (from w1) and r2 read k=v2 (from w2), and r1.Return < r2.Call, then w1 -> w2
	for k, vMap := range writerOfVal {
		for v1, w1 := range vMap {
			for v2, w2 := range vMap {
				if w1 == w2 || v1 == v2 {
					continue
				}
				// Find readers of v1 and v2
				for _, r1 := range txns {
					if r1.Status == history.Fail || !readsVal(r1, k, v1) {
						continue
					}
					for _, r2 := range txns {
						if r2.Status == history.Fail || !readsVal(r2, k, v2) {
							continue
						}
						if (r1.Return != history.Infinity && r1.Return < r2.Call) ||
							(r1.Process == r2.Process && r1.Return <= r2.Call && r1.ID < r2.ID) {
							recordWW(k, w1, w2)
						}
					}
				}
			}
		}
	}

	// 4. Infer RW (Anti-dependency) edges.
	// If T_r reads key k (either version v_w1 from w1, or initial state nil),
	// and T_w writes a later version of k (w1 ->ww w2, or initial read vs any writer of k):
	// then T_r anti-depends on T_w: T_r ──rw(k)──> T_w.
	for _, reader := range txns {
		if reader.Status == history.Fail {
			continue
		}
		for _, op := range reader.Ops {
			if op.Kind != OpRead {
				continue
			}
			k := op.Key
			if op.Value == nil {
				// Reader observed initial state: anti-depends on every writer of k
				for _, w := range writesByKey[k] {
					if reader.ID != w.txnID {
						g.AddEdge(Edge{
							From: reader.ID,
							To:   w.txnID,
							Kind: EdgeRW,
							Key:  k,
							Desc: fmt.Sprintf("T%d ──rw(%s)──> T%d", reader.ID, k, w.txnID),
						})
					}
				}
			} else {
				v := *op.Value
				w1 := writerOfVal[k][v]
				// Reader read from w1. For any w2 such that w1 ->ww w2 on key k, add reader ->rw w2.
				if wwPairs[k] != nil && wwPairs[k][w1] != nil {
					for w2 := range wwPairs[k][w1] {
						if reader.ID != w2 {
							g.AddEdge(Edge{
								From: reader.ID,
								To:   w2,
								Kind: EdgeRW,
								Key:  k,
								Desc: fmt.Sprintf("T%d ──rw(%s)──> T%d", reader.ID, k, w2),
							})
						}
					}
				}
			}
		}
	}

	// 5. Infer SO (Session Order) edges within the same client process
	byProcess := make(map[int][]Txn)
	for _, t := range txns {
		if t.Status == history.OK {
			byProcess[t.Process] = append(byProcess[t.Process], t)
		}
	}
	for _, list := range byProcess {
		sort.Slice(list, func(i, j int) bool {
			if list[i].Call != list[j].Call {
				return list[i].Call < list[j].Call
			}
			return list[i].ID < list[j].ID
		})
		for i := 0; i < len(list)-1; i++ {
			t1 := list[i]
			t2 := list[i+1]
			g.AddEdge(Edge{
				From: t1.ID,
				To:   t2.ID,
				Kind: EdgeSO,
				Desc: fmt.Sprintf("T%d ──so──> T%d", t1.ID, t2.ID),
			})
		}
	}

	// 6. Infer RO (Real-Time Order) edges: t1.Return < t2.Call
	for i := 0; i < len(txns); i++ {
		t1 := txns[i]
		if t1.Status != history.OK || t1.Return == history.Infinity {
			continue
		}
		for j := 0; j < len(txns); j++ {
			if i == j {
				continue
			}
			t2 := txns[j]
			if t2.Status != history.OK {
				continue
			}
			if t1.Return < t2.Call {
				g.AddEdge(Edge{
					From: t1.ID,
					To:   t2.ID,
					Kind: EdgeRO,
					Desc: fmt.Sprintf("T%d ──ro──> T%d", t1.ID, t2.ID),
				})
			}
		}
	}

	sort.Ints(g.Nodes)
	return g
}

func readsVal(t Txn, key string, val int64) bool {
	for _, op := range t.Ops {
		if op.Kind == OpRead && op.Key == key && op.Value != nil && *op.Value == val {
			return true
		}
	}
	return false
}
