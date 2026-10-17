package txn

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kemalhafizh/strife/internal/rng"
	"github.com/kemalhafizh/strife/pkg/history"
)

// ab builds list-append histories: each txn is "a k v" (append) or "r k
// v1,v2" (read) micro-ops separated by "; ".
type ab struct{ ops []history.Operation }

func (b *ab) txn(proc int, call, ret int64, status history.Type, spec string) *ab {
	var in, out []history.MicroOp
	for _, part := range strings.Split(spec, "; ") {
		f := strings.Fields(part)
		switch f[0] {
		case "a":
			var v int64
			fmt.Sscan(f[2], &v)
			m := history.MicroOp{F: history.MicroAppend, Key: f[1], Value: history.Int(v)}
			in, out = append(in, m), append(out, m)
		case "r":
			var vals []int64
			if len(f) > 2 {
				for _, s := range strings.Split(f[2], ",") {
					var v int64
					fmt.Sscan(s, &v)
					vals = append(vals, v)
				}
			}
			in = append(in, history.MicroOp{F: history.MicroRead, Key: f[1]})
			out = append(out, history.MicroOp{F: history.MicroRead, Key: f[1], Values: vals})
		}
	}
	if status == history.Info {
		ret = history.Infinity
		out = nil
	}
	id := len(b.ops)
	b.ops = append(b.ops, history.Operation{ID: id, Process: proc, Call: call, Return: ret,
		Input:  history.Op{Process: proc, Type: history.Invoke, F: history.FTxn, Txn: in},
		Output: history.Op{Process: proc, Type: status, F: history.FTxn, Txn: out}})
	return b
}

func types(r *Report) []AnomalyType {
	var out []AnomalyType
	for _, a := range r.Anomalies {
		out = append(out, a.Type)
	}
	return out
}

func expect(t *testing.T, ops []history.Operation, level IsolationLevel, want AnomalyType) *Report {
	t.Helper()
	r := CheckAppend(ops, level)
	if want == "" {
		if r.Verdict != Pass {
			t.Fatalf("%s: expected pass, got %v:\n%s", level, types(r), r.FullReport())
		}
		return r
	}
	if r.Verdict != Violation || !slices.Contains(types(r), want) {
		t.Fatalf("%s: expected %s, got %s %v:\n%s", level, want, r.Verdict, types(r), r.FullReport())
	}
	return r
}

const ok = history.OK

func TestAppendSerialHistoryPasses(t *testing.T) {
	b := &ab{}
	b.txn(0, 0, 10, ok, "a x 1; a y 1")
	b.txn(1, 11, 20, ok, "r x 1; a x 2")
	b.txn(0, 21, 30, ok, "r x 1,2; r y 1")
	b.txn(1, 31, 40, history.Fail, "a y 9")
	b.txn(2, 41, 50, history.Info, "a y 3")
	b.txn(0, 51, 60, ok, "r y 1,3")
	for _, l := range []IsolationLevel{LevelReadCommitted, LevelSnapshotIsolation, LevelSerializable, LevelStrictSerializable} {
		expect(t, b.ops, l, "")
	}
}

func TestAppendG1a(t *testing.T) {
	b := &ab{}
	b.txn(0, 0, 10, history.Fail, "a x 1")
	b.txn(1, 20, 30, ok, "r x 1")
	r := expect(t, b.ops, LevelReadCommitted, AnomalyG1a)
	if !strings.Contains(r.Anomalies[0].Explanation, "failed") {
		t.Fatal(r.Anomalies[0].Explanation)
	}
	expect(t, b.ops, LevelReadUncommitted, "")
}

func TestAppendG1b(t *testing.T) {
	b := &ab{}
	b.txn(0, 0, 10, ok, "a x 1; a x 2")
	b.txn(1, 5, 15, ok, "r x 1")
	expect(t, b.ops, LevelReadCommitted, AnomalyG1b)
}

func TestAppendG0(t *testing.T) {
	// T0 appends before T1 on x, after T1 on y.
	b := &ab{}
	b.txn(0, 0, 10, ok, "a x 1; a y 2")
	b.txn(1, 0, 10, ok, "a x 3; a y 4")
	b.txn(2, 20, 30, ok, "r x 1,3; r y 4,2")
	expect(t, b.ops, LevelReadUncommitted, AnomalyG0)
}

func TestAppendG1c(t *testing.T) {
	// Each observes the other's append.
	b := &ab{}
	b.txn(0, 0, 10, ok, "a x 1; r y 2")
	b.txn(1, 0, 10, ok, "a y 2; r x 1")
	expect(t, b.ops, LevelReadCommitted, AnomalyG1c)
}

func TestAppendReadSkewIsGSingle(t *testing.T) {
	// T2 sees T1's y but not its x: G-single, forbidden by SI.
	b := &ab{}
	b.txn(0, 0, 10, ok, "a x 1; a y 1")
	b.txn(1, 0, 10, ok, "r x; r y 1")
	b.txn(2, 20, 30, ok, "r x 1; r y 1")
	expect(t, b.ops, LevelSnapshotIsolation, AnomalyGSingle)
	expect(t, b.ops, LevelReadCommitted, "")
}

func TestAppendWriteSkewIsG2NotGSingle(t *testing.T) {
	// Classic write skew: allowed by snapshot isolation, not serializable.
	b := &ab{}
	b.txn(0, 0, 10, ok, "r x; a y 1")
	b.txn(1, 0, 10, ok, "r y; a x 2")
	b.txn(2, 20, 30, ok, "r x 2; r y 1")
	expect(t, b.ops, LevelSnapshotIsolation, "")
	expect(t, b.ops, LevelSerializable, AnomalyG2)
}

func TestAppendRealtime(t *testing.T) {
	// T1 starts after T0 completed but reads before T0's append: fine for
	// serializability (T1 may be ordered first), not for strict.
	b := &ab{}
	b.txn(0, 0, 10, ok, "a x 1")
	b.txn(1, 20, 30, ok, "r x")
	b.txn(2, 40, 50, ok, "r x 1")
	expect(t, b.ops, LevelSerializable, "")
	r := expect(t, b.ops, LevelStrictSerializable, AnomalyGRealtime)
	if !strings.Contains(r.Anomalies[0].Explanation, "completed at") {
		t.Fatal(r.Anomalies[0].Explanation)
	}
}

func TestAppendOrderAnomalies(t *testing.T) {
	b := &ab{}
	b.txn(0, 0, 10, ok, "a x 1")
	b.txn(1, 0, 10, ok, "a x 2")
	b.txn(2, 20, 30, ok, "r x 1,2")
	b.txn(3, 20, 30, ok, "r x 2,1")
	expect(t, b.ops, LevelReadUncommitted, AnomalyIncompatibleOrder)

	b = &ab{}
	b.txn(0, 0, 10, ok, "r x 7")
	expect(t, b.ops, LevelReadUncommitted, AnomalyPhantom)

	b = &ab{}
	b.txn(0, 0, 10, ok, "a x 1")
	b.txn(1, 20, 30, ok, "r x 1,1")
	expect(t, b.ops, LevelReadUncommitted, AnomalyDuplicate)

	b = &ab{}
	b.txn(0, 0, 10, ok, "a x 1; r x")
	expect(t, b.ops, LevelReadUncommitted, AnomalyInternal)
}

func TestAppendLostVersusStale(t *testing.T) {
	// Lost: acknowledged, then never observed by reads that began later.
	b := &ab{}
	b.txn(0, 0, 10, ok, "a x 1")
	b.txn(1, 0, 10, ok, "a x 2")
	b.txn(2, 20, 30, ok, "r x 2")
	r := expect(t, b.ops, LevelStrictSerializable, AnomalyLostAppend)
	if r.Anomalies[0].Type != AnomalyLostAppend || !strings.Contains(r.Anomalies[0].Explanation, "appended 1") {
		t.Fatal(r.FullReport())
	}
	// Stale: a later read observes it, so it was not lost.
	b.txn(3, 40, 50, ok, "r x 2,1")
	r = CheckAppend(b.ops, LevelStrictSerializable)
	if slices.Contains(types(r), AnomalyLostAppend) || r.Verdict != Violation {
		t.Fatalf("a stale read is not a lost append: %v", types(r))
	}
}

func TestAppendInfoTransactions(t *testing.T) {
	// An indeterminate append that was observed committed; one that was
	// never observed is simply unknown.
	b := &ab{}
	b.txn(0, 0, 10, history.Info, "a x 1")
	b.txn(1, 0, 10, history.Info, "a x 2")
	b.txn(2, 20, 30, ok, "r x 1")
	b.txn(3, 40, 50, ok, "r x 1")
	expect(t, b.ops, LevelStrictSerializable, "")
}

// TestAppendAgainstBruteForce checks soundness: whenever CheckAppend reports
// a strict-serializability violation, no serial order consistent with real
// time explains the history; and histories produced by a real serial
// execution always pass.
var appendSeed, appendIters uint64 = 7, 4000

func TestAppendAgainstBruteForce(t *testing.T) {
	r := rng.New(appendSeed)
	verdicts := map[bool]int{}
	for iter := 0; iter < int(appendIters); iter++ {
		ops, serial := randomAppendHistory(r, 2+r.Intn(4))
		rep := CheckAppend(ops, LevelStrictSerializable)
		if serial && rep.Verdict != Pass {
			t.Fatalf("iter %d: a serial execution was flagged:\n%s\n%s", iter, dumpTxns(ops), rep.FullReport())
		}
		if rep.Verdict == Violation && bruteStrict(ops) {
			t.Fatalf("iter %d: unsound %v, but a valid serial order exists:\n%s\n%s", iter, types(rep), dumpTxns(ops), rep.FullReport())
		}
		verdicts[rep.Verdict == Violation]++
	}
	if verdicts[true] < 200 || verdicts[false] < 200 {
		t.Fatalf("random histories not diverse enough: %v", verdicts)
	}
}

// TestAppendCompleteness measures how many histories with no valid strict
// serial order the checker flags. Unlike soundness this cannot be total: a
// cycle can hide behind an indeterminate transaction or a read the
// generator corrupted into something still explainable by a different
// version order. STRIFE_APPEND_ITERS raises the count for BENCHMARKS.md.
func TestAppendCompleteness(t *testing.T) {
	iters := 20000
	if testing.Short() {
		iters = 2000
	}
	if v, err := strconv.Atoi(os.Getenv("STRIFE_APPEND_ITERS")); err == nil && v > 0 {
		iters = v
	}
	r := rng.New(99)
	invalid, caught, valid, flagged := 0, 0, 0, 0
	for iter := 0; iter < iters; iter++ {
		ops, _ := randomAppendHistory(r, 2+r.Intn(4))
		v := CheckAppend(ops, LevelStrictSerializable).Verdict == Violation
		if bruteStrict(ops) {
			valid++
			if v {
				flagged++
			}
			continue
		}
		invalid++
		if v {
			caught++
		}
	}
	rate := float64(caught) / float64(invalid)
	t.Logf("invalid histories caught: %d/%d (%.2f%%); valid histories flagged: %d/%d", caught, invalid, 100*rate, flagged, valid)
	if flagged > 0 {
		t.Fatalf("%d valid histories flagged", flagged)
	}
	if rate < 0.99 {
		t.Fatalf("completeness %.2f%% below 99%%", 100*rate)
	}
}

// randomAppendHistory runs txns in a random serial order (each taking
// effect at an instant inside its interval), then sometimes corrupts one
// read. serial reports whether the history is untouched.
func randomAppendHistory(r *rng.Rand, n int) ([]history.Operation, bool) {
	keys := []string{"x", "y"}
	type tx struct {
		call, ret, at int64
		micro         []history.MicroOp
		status        history.Type
	}
	txs := make([]tx, n)
	next := int64(1)
	for i := range txs {
		call := r.Int64n(30)
		ret := call + 1 + r.Int64n(15)
		txs[i] = tx{call: call, ret: ret, at: call + r.Int64n(ret-call+1), status: history.OK}
		for j := 0; j < 1+r.Intn(3); j++ {
			k := keys[r.Intn(len(keys))]
			if r.Bool(0.5) {
				txs[i].micro = append(txs[i].micro, history.MicroOp{F: history.MicroAppend, Key: k, Value: history.Int(next)})
				next++
			} else {
				txs[i].micro = append(txs[i].micro, history.MicroOp{F: history.MicroRead, Key: k})
			}
		}
		switch x := r.Float64(); {
		case x < 0.1:
			txs[i].status = history.Fail
		case x < 0.2:
			txs[i].status = history.Info
		}
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return int(txs[a].at - txs[b].at) })
	state := map[string][]int64{}
	for _, i := range order {
		applies := txs[i].status == history.OK || (txs[i].status == history.Info && r.Bool(0.5))
		local := map[string][]int64{}
		for k, v := range state {
			local[k] = slices.Clone(v)
		}
		for j, m := range txs[i].micro {
			if m.F == history.MicroAppend {
				local[m.Key] = append(local[m.Key], *m.Value)
			} else {
				txs[i].micro[j].Values = slices.Clone(local[m.Key])
			}
		}
		if applies {
			state = local
		}
	}
	serial := true
	if r.Bool(0.5) {
		// Corrupt a read: drop its last element or swap two.
		for _, i := range r.Perm(n) {
			if txs[i].status != history.OK {
				continue
			}
			for j, m := range txs[i].micro {
				if m.F == history.MicroRead && len(m.Values) > 0 {
					txs[i].micro[j].Values = m.Values[:len(m.Values)-1]
					serial = false
					break
				}
			}
			if !serial {
				break
			}
		}
	}
	var ops []history.Operation
	for i, x := range txs {
		in := make([]history.MicroOp, len(x.micro))
		for j, m := range x.micro {
			in[j] = history.MicroOp{F: m.F, Key: m.Key, Value: m.Value}
		}
		o := history.Operation{ID: i, Process: i, Call: x.call, Return: x.ret,
			Input:  history.Op{Process: i, Type: history.Invoke, F: history.FTxn, Txn: in},
			Output: history.Op{Process: i, Type: x.status, F: history.FTxn}}
		switch x.status {
		case history.OK:
			o.Output.Txn = x.micro
		case history.Info:
			o.Return = history.Infinity
		}
		ops = append(ops, o)
	}
	return ops, serial
}

// bruteStrict reports whether some order of the ok txns, plus any subset of
// the info txns, consistent with real time, reproduces every read.
func bruteStrict(ops []history.Operation) bool {
	var cand []history.Operation
	for _, o := range ops {
		if o.Status() != history.Fail {
			cand = append(cand, o)
		}
	}
	used := make([]bool, len(cand))
	var rec func(state map[string][]int64) bool
	rec = func(state map[string][]int64) bool {
		done := true
		for i, o := range cand {
			if !used[i] && o.Status() == history.OK {
				done = false
			}
		}
		if done {
			return true
		}
		for i, o := range cand {
			if used[i] {
				continue
			}
			// i may go next only if no unused ok txn returned before i began.
			blocked := false
			for j, p := range cand {
				if !used[j] && j != i && p.Status() == history.OK && p.Return < o.Call {
					blocked = true
				}
			}
			if blocked {
				continue
			}
			next := map[string][]int64{}
			for k, v := range state {
				next[k] = slices.Clone(v)
			}
			good := true
			for _, m := range o.Micro() {
				if m.F == history.MicroAppend {
					next[m.Key] = append(next[m.Key], *m.Value)
				} else if o.Status() == history.OK && !slices.Equal(m.Values, next[m.Key]) {
					good = false
				}
			}
			if !good {
				continue
			}
			used[i] = true
			if rec(next) {
				return true
			}
			// An info txn may also never take effect.
			used[i] = false
		}
		// Info txns that never happen: allowed only if every ok txn is placed.
		return false
	}
	// Info txns are optional: mark them used (skipped) in every combination.
	var infos []int
	for i, o := range cand {
		if o.Status() == history.Info {
			infos = append(infos, i)
		}
	}
	for mask := 0; mask < 1<<len(infos); mask++ {
		for i := range used {
			used[i] = false
		}
		for b, i := range infos {
			if mask&(1<<b) != 0 {
				used[i] = true
			}
		}
		if rec(map[string][]int64{}) {
			return true
		}
	}
	return false
}

func dumpTxns(ops []history.Operation) string {
	var b strings.Builder
	for _, o := range ops {
		fmt.Fprintf(&b, "  [%d,%d] %s\n", o.Call, o.Return, o.Describe())
	}
	return b.String()
}

// bigAppendHistory is a valid strict-serializable history of n txns from
// procs concurrent processes over keys active keys. Like Strife's workload
// (and Elle's), a key is retired after 32 appends so lists stay short.
func bigAppendHistory(seed uint64, n, procs, keys int) []history.Operation {
	r := rng.New(seed)
	gen := make([]int, keys)
	writes := make([]int, keys)
	type tx struct {
		op history.Operation
		at int64
	}
	next := make([]int64, procs)
	var txs []tx
	for i := 0; i < n; i++ {
		p := r.Intn(procs)
		call := next[p] + r.Int64n(5)
		ret := call + 1 + r.Int64n(30)
		next[p] = ret + 1
		var micro []history.MicroOp
		for j := 0; j < 1+r.Intn(4); j++ {
			slot := r.Intn(keys)
			k := fmt.Sprintf("k%d.%d", slot, gen[slot])
			if r.Bool(0.5) {
				micro = append(micro, history.MicroOp{F: history.MicroAppend, Key: k, Value: history.Int(int64(i*8 + j))})
				if writes[slot]++; writes[slot] == 32 {
					gen[slot], writes[slot] = gen[slot]+1, 0
				}
			} else {
				micro = append(micro, history.MicroOp{F: history.MicroRead, Key: k})
			}
		}
		txs = append(txs, tx{history.Operation{Process: p, Call: call, Return: ret,
			Input:  history.Op{Process: p, Type: history.Invoke, F: history.FTxn, Txn: micro},
			Output: history.Op{Process: p, Type: history.OK, F: history.FTxn}}, call + r.Int64n(ret-call+1)})
	}
	slices.SortFunc(txs, func(a, b tx) int { return int(a.at - b.at) })
	state := map[string][]int64{}
	for i := range txs {
		out := make([]history.MicroOp, len(txs[i].op.Input.Txn))
		for j, m := range txs[i].op.Input.Txn {
			out[j] = m
			if m.F == history.MicroAppend {
				state[m.Key] = append(state[m.Key], *m.Value)
			} else {
				out[j].Values = slices.Clone(state[m.Key])
			}
		}
		txs[i].op.Output.Txn = out
	}
	ops := make([]history.Operation, len(txs))
	for i, t := range txs {
		ops[i] = t.op
	}
	slices.SortFunc(ops, func(a, b history.Operation) int { return int(a.Call - b.Call) })
	for i := range ops {
		ops[i].ID = i
	}
	return ops
}

func TestAppendLargeHistory(t *testing.T) {
	ops := bigAppendHistory(1, 20000, 10, 8)
	if r := CheckAppend(ops, LevelStrictSerializable); r.Verdict != Pass {
		t.Fatalf("large valid history flagged: %s", r.Summary())
	}
}

func BenchmarkCheckAppend(b *testing.B) {
	for _, n := range []int{1000, 10000, 50000} {
		ops := bigAppendHistory(1, n, 10, 8)
		b.Run(fmt.Sprintf("txns=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if r := CheckAppend(ops, LevelStrictSerializable); r.Verdict != Pass {
					b.Fatal(r.Summary())
				}
			}
			b.ReportMetric(float64(n)*float64(b.N)/b.Elapsed().Seconds(), "txns/s")
		})
	}
}
