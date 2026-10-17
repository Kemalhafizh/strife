package txn

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/kemalhafizh/strife/internal/rng"
	"github.com/kemalhafizh/strife/pkg/history"
)

// randomRegisterTxns runs random read/write transactions over two registers
// in a random serial order (each taking effect inside its interval), then
// sometimes corrupts one read. Written values are unique.
func randomRegisterTxns(r *rng.Rand, n int) ([]Txn, bool) {
	keys := []string{"x", "y"}
	txns := make([]Txn, n)
	at := make([]int64, n)
	next := int64(1)
	for i := range txns {
		call := r.Int64n(30)
		ret := call + 1 + r.Int64n(15)
		txns[i] = Txn{ID: i, Process: i, Status: history.OK, Call: call, Return: ret}
		at[i] = call + r.Int64n(ret-call+1)
		for j := 0; j < 1+r.Intn(3); j++ {
			k := keys[r.Intn(2)]
			if r.Bool(0.5) {
				txns[i].Ops = append(txns[i].Ops, MicroOp{Kind: OpWrite, Key: k, Value: history.Int(next)})
				next++
			} else {
				txns[i].Ops = append(txns[i].Ops, MicroOp{Kind: OpRead, Key: k})
			}
		}
		if r.Bool(0.1) {
			txns[i].Status = history.Fail
		}
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return int(at[a] - at[b]) })
	state := map[string]int64{}
	for _, i := range order {
		local := map[string]int64{}
		for k, v := range state {
			local[k] = v
		}
		for j, op := range txns[i].Ops {
			if op.Kind == OpWrite {
				local[op.Key] = *op.Value
			} else if v, ok := local[op.Key]; ok {
				txns[i].Ops[j].Value = history.Int(v)
			}
		}
		if txns[i].Status == history.OK {
			state = local
		}
	}
	serial := true
	if r.Bool(0.5) {
		for _, i := range r.Perm(n) {
			for j, op := range txns[i].Ops {
				if txns[i].Status == history.OK && op.Kind == OpRead && op.Value != nil && serial {
					// Pretend the read saw an older version: the initial state.
					txns[i].Ops[j].Value = nil
					serial = false
				}
			}
		}
	}
	return txns, serial
}

// bruteRegisters reports whether some order of the committed transactions
// (consistent with real time when realtime is set) reproduces every read.
func bruteRegisters(txns []Txn, realtime bool) bool {
	var cand []Txn
	for _, t := range txns {
		if t.Status == history.OK {
			cand = append(cand, t)
		}
	}
	used := make([]bool, len(cand))
	var rec func(state map[string]int64, placed int) bool
	rec = func(state map[string]int64, placed int) bool {
		if placed == len(cand) {
			return true
		}
		for i, t := range cand {
			if used[i] {
				continue
			}
			blocked := false
			for j, u := range cand {
				if realtime && !used[j] && j != i && u.Return < t.Call {
					blocked = true
				}
			}
			if blocked {
				continue
			}
			next := map[string]int64{}
			for k, v := range state {
				next[k] = v
			}
			good := true
			for _, op := range t.Ops {
				if op.Kind == OpWrite {
					next[op.Key] = *op.Value
					continue
				}
				v, ok := next[op.Key]
				if (op.Value == nil) != !ok || (ok && *op.Value != v) {
					good = false
				}
			}
			if !good {
				continue
			}
			used[i] = true
			if rec(next, placed+1) {
				return true
			}
			used[i] = false
		}
		return false
	}
	return rec(map[string]int64{}, 0)
}

// TestRegisterTxnAgainstBruteForce: the register checker must never flag a
// history that is valid at the level checked: strict serializability (real
// time respected) and plain serializability (any order).
func TestRegisterTxnAgainstBruteForce(t *testing.T) {
	r := rng.New(11)
	flagged := 0
	for iter := 0; iter < 40000; iter++ {
		txns, serial := randomRegisterTxns(r, 2+r.Intn(4))
		level, realtime := LevelStrictSerializable, true
		if iter%2 == 1 {
			level, realtime = LevelSerializable, false
		}
		rep := Check(txns, level)
		if rep.Verdict == Violation {
			flagged++
			if serial || bruteRegisters(txns, realtime) {
				var b strings.Builder
				for _, tx := range txns {
					fmt.Fprintf(&b, "  [%d,%d] %s\n", tx.Call, tx.Return, tx.Describe())
				}
				t.Fatalf("iter %d: unsound %s at %s on a valid history:\n%s%s", iter, rep.Anomalies[0].Type, level, b.String(), rep.FullReport())
			}
		}
	}
	if flagged < 1000 {
		t.Fatalf("only %d violations flagged: the generator is not exercising the checker", flagged)
	}
}
