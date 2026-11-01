package checker

import (
	"sort"

	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
)

// kvPruner tracks per-value observer and creator intervals for single-key
// register models ("kv") so the WGL search can prune unreachable value
// transitions and rank open candidates by earliest unsatisfied read deadline.
type kvPruner struct {
	ops            []history.Operation
	lin            *linSet
	stateToVID     map[uint64]int
	obsVID         []int
	createVID      []int
	soleCreator    []int
	effCall        []int64
	effReturn      []int64
	observersByRet [][]int
	destructByRet  [][]int
	creatorsByCall [][]int
	opsByRet       []int
	retRank        []int
}

func newKVPruner(m model.Model, ops []history.Operation, lin *linSet) *kvPruner {
	if m.Name() != "kv" {
		return nil
	}
	type vKey struct {
		set bool
		v   int64
	}
	vidMap := map[vKey]int{}
	stateToVID := map[uint64]int{}
	getVID := func(p *int64) int {
		var k vKey
		if p != nil {
			k = vKey{set: true, v: *p}
		}
		if id, ok := vidMap[k]; ok {
			return id
		}
		id := len(vidMap)
		vidMap[k] = id
		_, st := m.Step(m.Init(), history.Op{F: history.FWrite, Value: p}, history.Op{Type: history.OK})
		stateToVID[st.Hash()] = id
		return id
	}
	_ = getVID(nil) // initial nil register state is VID 0

	n := len(ops)
	obsVID := make([]int, n)
	createVID := make([]int, n)
	for i, o := range ops {
		obsVID[i] = -1
		createVID[i] = -1
		if o.Status() == history.OK {
			switch o.Input.F {
			case history.FRead:
				obsVID[i] = getVID(o.Output.Value)
			case history.FCas:
				obsVID[i] = getVID(o.Input.Old)
			}
		}
		switch o.Input.F {
		case history.FWrite:
			createVID[i] = getVID(o.Input.Value)
		case history.FCas:
			if o.Status() == history.OK || o.Status() == history.Info {
				if oldID, newID := getVID(o.Input.Old), getVID(o.Input.Value); newID != oldID {
					createVID[i] = newID
				}
			}
		}
	}

	numVals := len(vidMap)
	observersByRet := make([][]int, numVals)
	destructByRet := make([][]int, numVals)
	creatorsByCall := make([][]int, numVals)
	for i := range ops {
		if v := obsVID[i]; v >= 0 {
			observersByRet[v] = append(observersByRet[v], i)
			if createVID[i] >= 0 {
				destructByRet[v] = append(destructByRet[v], i)
			}
		}
		if v := createVID[i]; v >= 0 {
			creatorsByCall[v] = append(creatorsByCall[v], i)
		}
	}

	effCall := make([]int64, n)
	effReturn := make([]int64, n)
	soleCreator := make([]int, n)
	valWritten := make([]int, n)
	var defWrites []int
	for i, o := range ops {
		effCall[i] = o.Call
		effReturn[i] = o.Return
		soleCreator[i] = -1
		valWritten[i] = -1
		if o.Status() == history.OK && (o.Input.F == history.FWrite || o.Input.F == history.FCas) {
			valWritten[i] = getVID(o.Input.Value)
			defWrites = append(defWrites, i)
		}
	}

	dependsOn := func(w, target int) bool {
		for cur, hop := soleCreator[w], 0; hop < 8 && cur >= 0; hop++ {
			if cur == target {
				return true
			}
			cur = soleCreator[cur]
		}
		return false
	}

	// Tighten effective [effCall, effReturn] windows across value dependencies.
	for iter := 0; iter < 8; iter++ {
		sort.SliceStable(defWrites, func(a, b int) bool {
			return effReturn[defWrites[a]] < effReturn[defWrites[b]]
		})
		type topWrite struct {
			ret, call1, call2 int64
			val1              int
		}
		prefix := make([]topWrite, len(defWrites))
		var c1, c2 int64 = -1, -1
		v1 := -1
		for k, id := range defWrites {
			c, vw := effCall[id], valWritten[id]
			if vw == v1 {
				c1 = max(c1, c)
			} else if c > c1 {
				c2, c1, v1 = c1, c, vw
			} else if c > c2 {
				c2 = c
			}
			prefix[k] = topWrite{ret: effReturn[id], call1: c1, call2: c2, val1: v1}
		}
		overwriterCall := func(r, v int) int64 {
			if len(prefix) == 0 {
				return -1
			}
			rc := effCall[r]
			lo := sort.Search(len(prefix), func(i int) bool { return prefix[i].ret >= rc })
			if lo == 0 {
				return -1
			}
			if tw := prefix[lo-1]; tw.val1 != v {
				return tw.call1
			} else {
				return tw.call2
			}
		}

		changed := false
		for v := 1; v < numVals; v++ {
			crs, obs := creatorsByCall[v], observersByRet[v]
			if len(crs) == 0 || len(obs) == 0 {
				continue
			}
			for _, r := range obs {
				owCall := overwriterCall(r, v)
				var minC int64 = history.Infinity
				soleW, countW := -1, 0
				for _, w := range crs {
					if effReturn[w] < owCall || effCall[w] > effReturn[r] || dependsOn(w, r) {
						continue
					}
					minC = min(minC, effCall[w])
					soleW = w
					countW++
				}
				if minC < history.Infinity && minC > effCall[r] {
					effCall[r] = minC
					changed = true
				}
				if countW == 1 {
					if soleCreator[r] != soleW && !dependsOn(soleW, r) {
						soleCreator[r] = soleW
						changed = true
					}
					if effReturn[r] < effReturn[soleW] {
						effReturn[soleW] = effReturn[r]
						changed = true
					}
				}
			}
		}
		if !changed {
			break
		}
	}

	byRetThenCall := func(ids []int) {
		sort.SliceStable(ids, func(a, b int) bool {
			if effReturn[ids[a]] != effReturn[ids[b]] {
				return effReturn[ids[a]] < effReturn[ids[b]]
			}
			return effCall[ids[a]] < effCall[ids[b]]
		})
	}
	for v := 0; v < numVals; v++ {
		byRetThenCall(observersByRet[v])
		byRetThenCall(destructByRet[v])
		crs := creatorsByCall[v]
		sort.SliceStable(crs, func(a, b int) bool {
			if effCall[crs[a]] != effCall[crs[b]] {
				return effCall[crs[a]] < effCall[crs[b]]
			}
			return effReturn[crs[a]] < effReturn[crs[b]]
		})
	}

	opsByRet := make([]int, n)
	for i := range ops {
		opsByRet[i] = i
	}
	sort.SliceStable(opsByRet, func(a, b int) bool {
		if ops[opsByRet[a]].Return != ops[opsByRet[b]].Return {
			return ops[opsByRet[a]].Return < ops[opsByRet[b]].Return
		}
		return opsByRet[a] < opsByRet[b]
	})
	retRank := make([]int, n)
	for rank, id := range opsByRet {
		retRank[id] = rank
	}

	return &kvPruner{
		ops:            ops,
		lin:            lin,
		stateToVID:     stateToVID,
		obsVID:         obsVID,
		createVID:      createVID,
		soleCreator:    soleCreator,
		effCall:        effCall,
		effReturn:      effReturn,
		observersByRet: observersByRet,
		destructByRet:  destructByRet,
		creatorsByCall: creatorsByCall,
		opsByRet:       opsByRet,
		retRank:        retRank,
	}
}

func (p *kvPruner) vid(s model.State) int {
	if id, ok := p.stateToVID[s.Hash()]; ok {
		return id
	}
	return -1
}

func (p *kvPruner) dependsOn(w, target int) bool {
	for cur, hop := p.soleCreator[w], 0; hop < 8 && cur >= 0; hop++ {
		if cur == target {
			return true
		}
		cur = p.soleCreator[cur]
	}
	return false
}

func (p *kvPruner) minRemCreatorCall2(v, ex1, ex2 int) int64 {
	for _, w := range p.creatorsByCall[v] {
		if w != ex1 && w != ex2 && !p.lin.has(w) {
			return p.effCall[w]
		}
	}
	return history.Infinity
}

func (p *kvPruner) minRemCreatorCallFor(v, excludeID, target, nextV int) int64 {
	tRet := p.effReturn[target]
	for _, w := range p.creatorsByCall[v] {
		if w == excludeID || w == target || p.lin.has(w) || p.dependsOn(w, target) {
			continue
		}
		if p.effCall[w] > tRet {
			return p.effCall[w]
		}
		if u := p.obsVID[w]; u >= 0 && u != nextV {
			limit := min(tRet, p.effReturn[w])
			if p.minRemCreatorCall2(u, excludeID, target) > limit {
				continue
			}
		}
		return p.effCall[w]
	}
	return history.Infinity
}

// checkDeadGap matches remaining creators of v against remaining destructive
// CASes of v (sorted by effReturn) and checks that no observer of v is trapped
// in the dead gap between the end of the c-th visit and the start of the
// (c+1)-th creator. Returns the first destructive CAS id and minimum call time
// when a deficit or dead gap occurs, or (-1, Infinity) otherwise.
func (p *kvPruner) checkDeadGap(v, opID int) (int, int64) {
	if len(p.destructByRet[v]) == 0 {
		return -1, history.Infinity
	}
	var remW []int64
	for _, w := range p.creatorsByCall[v] {
		if w != opID && !p.lin.has(w) {
			remW = append(remW, p.effCall[w])
		}
	}
	firstD := -1
	var minDCall int64 = history.Infinity
	wIdx := 0
	for _, d := range p.destructByRet[v] {
		if d == opID || p.lin.has(d) {
			continue
		}
		if firstD < 0 {
			firstD = d
		}
		minDCall = min(minDCall, p.effCall[d])
		var wCall int64 = history.Infinity
		if wIdx < len(remW) {
			wCall = remW[wIdx]
			wIdx++
		}
		dRet := p.effReturn[d]
		if wCall > dRet {
			return firstD, minDCall
		}
		var gapEnd int64 = history.Infinity
		if wIdx < len(remW) {
			gapEnd = remW[wIdx]
		}
		if gapEnd > dRet {
			for _, r := range p.observersByRet[v] {
				if r == opID || p.lin.has(r) {
					continue
				}
				if p.effReturn[r] >= gapEnd {
					break
				}
				if p.effCall[r] > dRet {
					return firstD, minDCall
				}
			}
		}
	}
	return -1, history.Infinity
}

func (p *kvPruner) firstBlocker(m model.Model, opID int, curState, nextState model.State, rMin *entry) int {
	curV, nextV := p.vid(curState), p.vid(nextState)
	if curV < 0 || nextV < 0 {
		return -1
	}

	if curV != nextV {
		for _, r := range p.observersByRet[curV] {
			if r != opID && !p.lin.has(r) && p.minRemCreatorCallFor(curV, opID, r, nextV) > p.effReturn[r] {
				return r
			}
		}
		if firstD, _ := p.checkDeadGap(curV, opID); firstD >= 0 {
			return firstD
		}
	}

	if rMin != nil && (curV != nextV || p.createVID[opID] == nextV) {
		var tHold int64 = -1
		for _, r := range p.observersByRet[nextV] {
			if r != opID && !p.lin.has(r) && p.minRemCreatorCallFor(nextV, opID, r, nextV) > p.effReturn[r] {
				tHold = max(tHold, p.effCall[r])
			}
		}
		if firstD, minDCall := p.checkDeadGap(nextV, opID); firstD >= 0 {
			tHold = max(tHold, minDCall)
		}
		if tHold > rMin.time {
			for k := p.retRank[rMin.id]; k < len(p.opsByRet); k++ {
				j := p.opsByRet[k]
				if p.ops[j].Return >= tHold {
					break
				}
				if j == opID || p.lin.has(j) {
					continue
				}
				if okJ, afterJ := m.Step(nextState, p.ops[j].Input, p.ops[j].Output); !okJ || !afterJ.Equal(nextState) {
					return j
				}
			}
		}
	}

	return -1
}

func (p *kvPruner) canTransition(m model.Model, opID int, curState, nextState model.State, rMin *entry) bool {
	return p.firstBlocker(m, opID, curState, nextState, rMin) < 0
}

func (p *kvPruner) rankCandidates(m model.Model, curState model.State, headNext, rMin *entry, cands []cand) {
	if p == nil || rMin == nil || len(cands) <= 1 {
		return
	}
	curV := p.vid(curState)
	if curV < 0 {
		return
	}

	targetOp := rMin.id
	seen := make(map[int]bool, 16)
	for hop := 0; hop < 16 && !seen[targetOp]; hop++ {
		seen[targetOp] = true
		ok, next := m.Step(curState, p.ops[targetOp].Input, p.ops[targetOp].Output)
		if !ok {
			wantV := p.obsVID[targetOp]
			if wantV < 0 || wantV == curV {
				break
			}
			bestW := -1
			for _, w := range p.creatorsByCall[wantV] {
				if w == targetOp || p.lin.has(w) || p.ops[w].Call > rMin.time {
					continue
				}
				if okW, nextW := m.Step(curState, p.ops[w].Input, p.ops[w].Output); okW && p.firstBlocker(m, w, curState, nextW, rMin) < 0 {
					bestW = w
					break
				}
				if bestW < 0 || p.effReturn[w] < p.effReturn[bestW] {
					bestW = w
				}
			}
			if bestW < 0 {
				break
			}
			targetOp = bestW
			continue
		}
		b := p.firstBlocker(m, targetOp, curState, next, rMin)
		if b < 0 {
			break
		}
		targetOp = b
	}

	wantV := p.obsVID[targetOp]
	var dist []int
	if wantV >= 0 && curV != wantV {
		dist = make([]int, len(p.observersByRet))
		for i := range dist {
			dist[i] = -1
		}
		dist[wantV] = 0
		queue := []int{wantV}
		for len(queue) > 0 {
			v := queue[0]
			queue = queue[1:]
			for c := headNext; c != nil && c.call; c = c.next {
				if p.ops[c.id].Input.F == history.FCas && p.createVID[c.id] == v {
					if u := p.obsVID[c.id]; u >= 0 && dist[u] < 0 {
						dist[u] = dist[v] + 1
						queue = append(queue, u)
					}
				}
			}
		}
	}

	for i := range cands {
		id := cands[i].e.id
		nextV := p.vid(cands[i].next)
		hasOpenObs := false
		if nextV >= 0 {
			for _, r := range p.observersByRet[nextV] {
				if r != id && !p.lin.has(r) && p.ops[r].Call <= rMin.time {
					hasOpenObs = true
					break
				}
			}
		}
		if p.ops[id].Input.F == history.FCas {
			cands[i].lock = false
			switch {
			case id == targetOp:
				cands[i].prio = -1_000_000_000
			case dist != nil && nextV >= 0 && dist[nextV] >= 0:
				cands[i].prio = -500_000_000 + int64(dist[nextV])
			default:
				cands[i].prio = -100_000_000 + p.effReturn[id]
			}
			continue
		}
		if id == targetOp {
			cands[i].lock = false
			cands[i].prio = -2
			continue
		}
		if dist != nil && nextV >= 0 && dist[nextV] >= 0 {
			cands[i].lock = false
			cands[i].prio = int64(dist[nextV])
			continue
		}
		cands[i].lock = !hasOpenObs && id != rMin.id
		cands[i].prio = 1_000_000_000_000 + p.effReturn[id]
	}
}
