package txn

import (
	"fmt"
	"strings"
)

// Cycle represents a directed cycle in the transaction dependency graph.
type Cycle struct {
	Nodes []int  `json:"nodes"` // Txn IDs in traversal order, with Nodes[0] == Nodes[len-1]
	Edges []Edge `json:"edges"` // Edges connecting Nodes[i] -> Nodes[i+1]
}

// String renders the cycle compactly with arrows and edge labels, e.g.
// "T1 ──wr(x=1)──> T2 ──rw(y)──> T1".
func (c Cycle) String() string {
	if len(c.Edges) == 0 {
		return ""
	}
	var b strings.Builder
	for i, e := range c.Edges {
		if i > 0 {
			b.WriteString(" ")
		}
		val := ""
		if e.Value != nil {
			val = fmt.Sprintf("=%d", *e.Value)
		}
		if e.Key != "" {
			fmt.Fprintf(&b, "T%d ──%s(%s%s)──> ", e.From, e.Kind, e.Key, val)
		} else {
			fmt.Fprintf(&b, "T%d ──%s──> ", e.From, e.Kind)
		}
	}
	fmt.Fprintf(&b, "T%d", c.Edges[len(c.Edges)-1].To)
	return b.String()
}

// HasKind reports whether any edge in the cycle has the given kind.
func (c Cycle) HasKind(kind EdgeKind) bool {
	for _, e := range c.Edges {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// OnlyKinds reports whether all edges in the cycle belong to the allowed set of kinds.
func (c Cycle) OnlyKinds(allowed ...EdgeKind) bool {
	set := make(map[EdgeKind]bool, len(allowed))
	for _, a := range allowed {
		set[a] = true
	}
	for _, e := range c.Edges {
		if !set[e.Kind] {
			return false
		}
	}
	return true
}

// FindCycles discovers minimal directed cycles in g.
// It uses Tarjan's Strongly Connected Components algorithm to isolate cyclic
// clusters, and BFS to find the shortest simple cycle in each component.
func FindCycles(g *Graph) []Cycle {
	sccs := tarjanSCC(g)
	var cycles []Cycle

	for _, scc := range sccs {
		if len(scc) < 2 {
			// Check for self-loop if single node
			if len(scc) == 1 {
				u := scc[0]
				for _, e := range g.Adj[u] {
					if e.To == u {
						cycles = append(cycles, Cycle{
							Nodes: []int{u, u},
							Edges: []Edge{e},
						})
						break
					}
				}
			}
			continue
		}

		// Find the shortest cycle within this SCC
		sccSet := make(map[int]bool, len(scc))
		for _, u := range scc {
			sccSet[u] = true
		}

		var shortest *Cycle
		for _, start := range scc {
			c := shortestCycleFrom(g, start, sccSet)
			if c != nil {
				if shortest == nil || len(c.Edges) < len(shortest.Edges) {
					shortest = c
				}
			}
		}
		if shortest != nil {
			cycles = append(cycles, *shortest)
		}
	}
	return cycles
}

// shortestCycleFrom finds the shortest simple cycle starting and ending at node start.
func shortestCycleFrom(g *Graph, start int, allowedNodes map[int]bool) *Cycle {
	type queueItem struct {
		node  int
		path  []int
		edges []Edge
	}

	queue := []queueItem{{node: start, path: []int{start}, edges: nil}}
	visited := make(map[int]bool)

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		for _, e := range g.Adj[curr.node] {
			next := e.To
			if !allowedNodes[next] {
				continue
			}

			if next == start && len(curr.edges) > 0 {
				// Found cycle back to start!
				newPath := append(append([]int(nil), curr.path...), start)
				newEdges := append(append([]Edge(nil), curr.edges...), e)
				return &Cycle{Nodes: newPath, Edges: newEdges}
			}

			if !visited[next] {
				visited[next] = true
				newPath := append(append([]int(nil), curr.path...), next)
				newEdges := append(append([]Edge(nil), curr.edges...), e)
				queue = append(queue, queueItem{
					node:  next,
					path:  newPath,
					edges: newEdges,
				})
			}
		}
	}
	return nil
}

// tarjanSCC partitions the graph vertices into strongly connected components.
func tarjanSCC(g *Graph) [][]int {
	var index int
	indices := make(map[int]int)
	lowlink := make(map[int]int)
	onStack := make(map[int]bool)
	var stack []int
	var sccs [][]int

	var strongconnect func(u int)
	strongconnect = func(u int) {
		indices[u] = index
		lowlink[u] = index
		index++
		stack = append(stack, u)
		onStack[u] = true

		for _, e := range g.Adj[u] {
			v := e.To
			if _, visited := indices[v]; !visited {
				strongconnect(v)
				if lowlink[v] < lowlink[u] {
					lowlink[u] = lowlink[v]
				}
			} else if onStack[v] {
				if indices[v] < lowlink[u] {
					lowlink[u] = indices[v]
				}
			}
		}

		if lowlink[u] == indices[u] {
			var scc []int
			for {
				v := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[v] = false
				scc = append(scc, v)
				if v == u {
					break
				}
			}
			sccs = append(sccs, scc)
		}
	}

	for _, u := range g.Nodes {
		if _, visited := indices[u]; !visited {
			strongconnect(u)
		}
	}
	return sccs
}
