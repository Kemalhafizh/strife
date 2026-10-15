package shrink

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/model"
)

// HistoryOptions tunes the history reduction search.
type HistoryOptions struct {
	MaxChecks int           // bounds total checker invocations; 0 means 4000
	MaxSteps  uint64        // search budget per candidate check; 0 means 1,000,000
	Timeout   time.Duration // per-check timeout; 0 means no timeout
	Progress  func(HistoryStep)
}

// HistoryStep records progress after one reduction phase.
type HistoryStep struct {
	Phase string `json:"phase"`
	Ops   int    `json:"ops"`
	Note  string `json:"note"`
}

// HistoryResult is the outcome of history shrinking.
type HistoryResult struct {
	OriginalOps int                 `json:"original_ops"`
	MinimalOps  int                 `json:"minimal_ops"`
	Key         string              `json:"key"`
	Verdict     checker.Verdict     `json:"verdict"`
	Diagnosis   string              `json:"diagnosis,omitempty"`
	Explanation string              `json:"explanation,omitempty"`
	Steps       []HistoryStep       `json:"steps"`
	History     []history.Op        `json:"-"`
	Operations  []history.Operation `json:"-"`
	Elapsed     time.Duration       `json:"elapsed"`
}

// History shrinks a raw history down to the minimal sound subset of operations
// that still violates linearizability with respect to m.
func History(ctx context.Context, m model.Model, raw []history.Op, opts HistoryOptions) (*HistoryResult, error) {
	start := time.Now()
	if opts.MaxChecks <= 0 {
		opts.MaxChecks = 4000
	}
	if opts.MaxSteps == 0 {
		opts.MaxSteps = 1_000_000
	}

	progress := opts.Progress
	if progress == nil {
		progress = func(HistoryStep) {}
	}

	paired, err := history.Pair(raw)
	if err != nil {
		return nil, fmt.Errorf("history shrink: pairing error: %w", err)
	}

	res := &HistoryResult{
		OriginalOps: len(paired),
		Verdict:     checker.Linearizable,
	}

	step := func(phase, note string, count int) {
		s := HistoryStep{Phase: phase, Ops: count, Note: note}
		res.Steps = append(res.Steps, s)
		progress(s)
	}

	step("baseline", fmt.Sprintf("analyzing %d operations", len(paired)), len(paired))

	// Phase 1: Partitioning. Identify the violating partition (e.g. key) and isolate it.
	var relevant []history.Operation
	for _, o := range paired {
		if m.Relevant(o) {
			relevant = append(relevant, o)
		}
	}

	keys, parts := m.Partition(relevant)
	var violatingKey string
	var violatingOps []history.Operation

	for i, key := range keys {
		part := parts[i]
		chk := checker.CheckOperations(m, part, checker.Options{
			MaxSteps: opts.MaxSteps,
			Timeout:  opts.Timeout,
		})
		if chk.Verdict == checker.Violation {
			violatingKey = key
			violatingOps = part
			res.Verdict = checker.Violation
			break
		}
	}

	if res.Verdict != checker.Violation {
		return nil, ErrNotViolating
	}

	res.Key = violatingKey
	step("partition", fmt.Sprintf("isolated violating partition %q: %d → %d ops", violatingKey, len(paired), len(violatingOps)), len(violatingOps))

	// Phase 2: Model Diagnoser shortcut.
	// If the model provides a sound, provable certificate (e.g. stale register read or cycle),
	// verify if that witness alone violates.
	if d, ok := m.(model.Diagnoser); ok {
		if idx, diag, ok := d.Diagnose(violatingOps); ok && len(idx) > 0 {
			witness := make([]history.Operation, len(idx))
			for i, j := range idx {
				witness[i] = violatingOps[j]
			}
			chk := checker.CheckOperations(m, witness, checker.Options{
				MaxSteps: opts.MaxSteps,
				Timeout:  opts.Timeout,
			})
			if chk.Verdict == checker.Violation {
				sort.Slice(witness, func(i, j int) bool { return witness[i].Call < witness[j].Call })
				res.Diagnosis = diag
				res.MinimalOps = len(witness)
				res.Operations = witness
				res.History = reconstruct(witness)
				res.Elapsed = time.Since(start)
				step("diagnose", fmt.Sprintf("sound diagnoser extracted %d-op minimal witness: %s", len(witness), diag), len(witness))
				return res, nil
			}
		}
	}

	// Phase 3: Generic Sound Delta Debugging.
	checksRemaining := opts.MaxChecks
	fails := func(candidate []history.Operation) bool {
		if ctx.Err() != nil || checksRemaining <= 0 {
			return false
		}
		checksRemaining--
		if !isSoundHistory(m, candidate) {
			return false
		}
		chk := checker.CheckOperations(m, candidate, checker.Options{
			MaxSteps: opts.MaxSteps,
			Timeout:  opts.Timeout,
		})
		return chk.Verdict == checker.Violation
	}

	// 3a. Pure-read minimisation: dropping pure reads is always sound.
	var muts, reads []history.Operation
	for _, o := range violatingOps {
		if isReadOnly(o) {
			reads = append(reads, o)
		} else {
			muts = append(muts, o)
		}
	}

	failsReads := func(subReads []history.Operation) bool {
		cand := append(append([]history.Operation(nil), muts...), subReads...)
		return fails(cand)
	}

	if failsReads(reads) {
		minReads := runDDMin(reads, failsReads)
		violatingOps = append(append([]history.Operation(nil), muts...), minReads...)
		sort.Slice(violatingOps, func(i, j int) bool { return violatingOps[i].Call < violatingOps[j].Call })
		step("pure-reads", fmt.Sprintf("reduced pure reads: %d remaining", len(violatingOps)), len(violatingOps))
	}

	// 3b. Sound full-history ddmin over all remaining operations.
	cur := runDDMin(violatingOps, fails)
	step("ddmin", fmt.Sprintf("delta debugging: %d remaining", len(cur)), len(cur))

	// Phase 4: 1-minimality verification pass.
	// Try dropping each single operation: if dropping it is sound and still violates, drop it.
	improved := true
	for improved && len(cur) > 1 {
		improved = false
		for i := 0; i < len(cur); i++ {
			cand := make([]history.Operation, 0, len(cur)-1)
			cand = append(cand, cur[:i]...)
			cand = append(cand, cur[i+1:]...)
			if fails(cand) {
				cur = cand
				improved = true
				break
			}
		}
	}

	sort.Slice(cur, func(i, j int) bool { return cur[i].Call < cur[j].Call })
	step("verify", fmt.Sprintf("1-minimal verification: %d ops", len(cur)), len(cur))

	// Compute final check and explanation
	finalCheck := checker.CheckOperations(m, cur, checker.Options{MaxSteps: opts.MaxSteps, Timeout: opts.Timeout})
	for _, k := range finalCheck.Keys {
		if k.Verdict == checker.Violation && k.Counterexample != nil {
			res.Explanation = k.Counterexample.Summary()
			break
		}
	}

	res.MinimalOps = len(cur)
	res.Operations = cur
	res.History = reconstruct(cur)
	res.Elapsed = time.Since(start)
	return res, nil
}

func isReadOnly(o history.Operation) bool {
	f := o.Input.F
	return f == history.FRead || f == "get"
}

// isSoundHistory ensures candidate does not contain reads observing values
// that were never written by any preceding or concurrent operation.
func isSoundHistory(m model.Model, ops []history.Operation) bool {
	if len(ops) == 0 {
		return false
	}

	// Track values written
	written := make(map[int64]bool)
	written[0] = true // initial register value

	for _, o := range ops {
		f := o.Input.F
		if f == history.FWrite || f == history.FCas || f == history.FAdd || f == history.FEnqueue {
			if o.Input.Value != nil {
				written[*o.Input.Value] = true
			}
		}
	}

	for _, o := range ops {
		if isReadOnly(o) && o.Status() == history.OK && o.Output.Value != nil {
			v := *o.Output.Value
			if !written[v] {
				// Observed a value never produced by any write in this sub-history
				return false
			}
		}
		if o.Input.F == history.FDequeue && o.Status() == history.OK && o.Output.Value != nil {
			v := *o.Output.Value
			if !written[v] {
				return false
			}
		}
	}
	return true
}

func runDDMin[T any](items []T, fails func([]T) bool) []T {
	if len(items) <= 1 {
		return items
	}
	n := 2
	for len(items) >= 2 {
		chunks := makeChunks(items, n)
		reduced := false
		for _, c := range chunks {
			if fails(c) {
				items, n, reduced = c, 2, true
				break
			}
		}
		if !reduced && len(chunks) > 2 {
			for i := range chunks {
				comp := omitChunk(chunks, i)
				if fails(comp) {
					items, n, reduced = comp, max(n-1, 2), true
					break
				}
			}
		}
		if reduced {
			continue
		}
		if n >= len(items) {
			break
		}
		n = min(2*n, len(items))
	}
	return items
}

func makeChunks[T any](items []T, n int) [][]T {
	var out [][]T
	size, rem := len(items)/n, len(items)%n
	at := 0
	for i := 0; i < n; i++ {
		k := size
		if i < rem {
			k++
		}
		if k == 0 {
			continue
		}
		out = append(out, items[at:at+k])
		at += k
	}
	return out
}

func omitChunk[T any](chunks [][]T, skip int) []T {
	var out []T
	for i, c := range chunks {
		if i != skip {
			out = append(out, c...)
		}
	}
	return out
}

// reconstruct turns paired operations back into a sequential stream of history.Op events.
func reconstruct(ops []history.Operation) []history.Op {
	type rawItem struct {
		time int64
		op   history.Op
	}
	var items []rawItem
	for _, o := range ops {
		items = append(items, rawItem{time: o.Call, op: o.Input})
		if o.Return != history.Infinity {
			items = append(items, rawItem{time: o.Return, op: o.Output})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].time != items[j].time {
			return items[i].time < items[j].time
		}
		if items[i].op.Type != items[j].op.Type {
			return items[i].op.Type == history.Invoke
		}
		return items[i].op.Process < items[j].op.Process
	})

	out := make([]history.Op, len(items))
	for i, item := range items {
		out[i] = item.op
		out[i].Index = i
	}
	return out
}
