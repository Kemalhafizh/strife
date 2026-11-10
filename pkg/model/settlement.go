package model

import (
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"strings"

	"github.com/kemalhafizh/strife/pkg/history"
)

// Cross-system settlement and transactional-outbox anomaly identifiers.
const (
	AnomalySettlementAtomicity   = "SETTLEMENT-ATOMICITY"
	AnomalySettlementOrphan      = "SETTLEMENT-NO-ORPHAN-CREDIT"
	AnomalySettlementIdempotency = "SETTLEMENT-IDEMPOTENCY"
)

// Settlement models a two-leg cross-system settlement or transactional outbox
// pipeline:
//
//   - Source leg ("ledger", "source", "debit"): records committed ledger
//     settlement IDs.
//   - Destination leg ("broker", "clearing", "sink", "credit"): records
//     published/cleared settlement IDs delivered to the downstream system.
//
// It verifies three invariants:
//  1. SETTLEMENT-ATOMICITY: every committed source settlement ID is present in
//     the destination clearing log by the time a post-commit audit/read
//     observes the system (no dropped outbox message on dual-write crash).
//  2. SETTLEMENT-NO-ORPHAN-CREDIT: no settlement ID appears in the destination
//     clearing log unless it was committed to the source ledger (no phantom
//     credit from publish-before-commit rollback).
//  3. SETTLEMENT-IDEMPOTENCY: no settlement ID is applied more than once to
//     either the source ledger or the destination clearing log across retries.
type Settlement struct{}

type settlementState struct {
	ledger []int64
	broker []int64
}

func (s settlementState) Hash() uint64 {
	h := fnv.New64a()
	for _, v := range s.ledger {
		_, _ = fmt.Fprintf(h, "L:%d;", v)
	}
	for _, v := range s.broker {
		_, _ = fmt.Fprintf(h, "B:%d;", v)
	}
	return h.Sum64()
}

func (s settlementState) Equal(o State) bool {
	t, ok := o.(settlementState)
	if !ok {
		return false
	}
	return slices.Equal(s.ledger, t.ledger) && slices.Equal(s.broker, t.broker)
}

func (s settlementState) String() string {
	return fmt.Sprintf("ledger=%v broker=%v", s.ledger, s.broker)
}

// Name implements Model.
func (Settlement) Name() string { return "settlement" }

// Init implements Model.
func (Settlement) Init() State { return settlementState{} }

// Relevant implements Model.
func (Settlement) Relevant(o history.Operation) bool {
	return o.Status() != history.Fail
}

// Partition implements Model. Cross-system settlement invariants span both the
// source ledger and the destination broker, so the history is checked as a
// single holistic partition.
func (Settlement) Partition(ops []history.Operation) ([]string, [][]history.Operation) {
	if len(ops) == 0 {
		return nil, nil
	}
	return []string{"settlement"}, [][]history.Operation{ops}
}

func isSourceLegKey(k string) bool {
	k = strings.ToLower(k)
	return k == "ledger" || strings.HasPrefix(k, "ledger:") ||
		k == "source" || strings.HasPrefix(k, "source:") ||
		k == "debit" || strings.HasPrefix(k, "debit:") ||
		k == "outbox" || strings.HasPrefix(k, "outbox:")
}

func isDestLegKey(k string) bool {
	k = strings.ToLower(k)
	return k == "broker" || strings.HasPrefix(k, "broker:") ||
		k == "clearing" || strings.HasPrefix(k, "clearing:") ||
		k == "sink" || strings.HasPrefix(k, "sink:") ||
		k == "credit" || strings.HasPrefix(k, "credit:")
}

// Step implements Model.
func (Settlement) Step(s State, in, out history.Op) (bool, State) {
	st := s.(settlementState)
	next := settlementState{
		ledger: append([]int64(nil), st.ledger...),
		broker: append([]int64(nil), st.broker...),
	}

	op := out
	if len(op.Txn) == 0 {
		op = in
	}
	if in.F == history.FTxn || len(op.Txn) > 0 {
		var wroteLedger, wroteBroker bool
		for _, m := range op.Txn {
			switch m.F {
			case history.MicroAppend, history.MicroWrite:
				if m.Value == nil {
					continue
				}
				v := *m.Value
				if isSourceLegKey(m.Key) {
					if slices.Contains(next.ledger, v) {
						return false, st
					}
					next.ledger = append(next.ledger, v)
					wroteLedger = true
				} else if isDestLegKey(m.Key) {
					if slices.Contains(next.broker, v) {
						return false, st
					}
					next.broker = append(next.broker, v)
					wroteBroker = true
				} else {
					if slices.Contains(next.ledger, v) || slices.Contains(next.broker, v) {
						return false, st
					}
					next.ledger = append(next.ledger, v)
					next.broker = append(next.broker, v)
					wroteLedger = true
					wroteBroker = true
				}
			case history.MicroRead:
				if out.Type != history.OK {
					continue
				}
				if isSourceLegKey(m.Key) && !slices.Equal(next.ledger, m.Values) {
					return false, st
				}
				if isDestLegKey(m.Key) && !slices.Equal(next.broker, m.Values) {
					return false, st
				}
			}
		}
		if wroteLedger && wroteBroker && out.Type == history.OK && !slices.Equal(next.ledger, next.broker) {
			return false, st
		}
		return true, next
	}

	switch in.F {
	case history.FWrite, history.FAdd, history.FEnqueue, "settle":
		if in.Value == nil {
			return false, st
		}
		v := *in.Value
		if isSourceLegKey(in.Key) {
			if slices.Contains(next.ledger, v) {
				return false, st
			}
			next.ledger = append(next.ledger, v)
		} else if isDestLegKey(in.Key) {
			if slices.Contains(next.broker, v) {
				return false, st
			}
			next.broker = append(next.broker, v)
		} else {
			if slices.Contains(next.ledger, v) || slices.Contains(next.broker, v) {
				return false, st
			}
			next.ledger = append(next.ledger, v)
			next.broker = append(next.broker, v)
		}
		return true, next
	case history.FRead:
		if out.Type != history.OK {
			return true, st
		}
		if isSourceLegKey(in.Key) {
			return slices.Equal(st.ledger, out.Values), st
		}
		if isDestLegKey(in.Key) {
			return slices.Equal(st.broker, out.Values), st
		}
	}
	return true, next
}

// Decide implements Decider for Settlement.
func (Settlement) Decide(ops []history.Operation) (bool, []int, string, []string) {
	var (
		kinds     []string
		witness   []int
		diagnosis string
	)
	addKind := func(k string) {
		if !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
	}
	recordViolation := func(k string, w []int, diag string) {
		addKind(k)
		if diagnosis == "" {
			witness = w
			diagnosis = diag
		}
	}

	committedLedger := make(map[int64]int) // txID -> op index that committed it
	possibleLedger := make(map[int64]int)  // txID -> op index (ok or info)
	committedBroker := make(map[int64]int)
	ledgerWriteCount := make(map[int64][]int)
	brokerWriteCount := make(map[int64][]int)

	for i, o := range ops {
		st := o.Status()
		if st == history.Fail {
			continue
		}
		// Inspect transaction micro-ops
		micro := o.Output.Txn
		if len(micro) == 0 {
			micro = o.Input.Txn
		}
		if len(micro) > 0 {
			for _, m := range micro {
				switch m.F {
				case history.MicroAppend, history.MicroWrite:
					if m.Value == nil {
						continue
					}
					v := *m.Value
					if isSourceLegKey(m.Key) {
						possibleLedger[v] = i
						if st == history.OK {
							if _, exists := committedLedger[v]; !exists {
								committedLedger[v] = i
							}
							ledgerWriteCount[v] = append(ledgerWriteCount[v], i)
						}
					} else if isDestLegKey(m.Key) {
						if st == history.OK {
							if _, exists := committedBroker[v]; !exists {
								committedBroker[v] = i
							}
							brokerWriteCount[v] = append(brokerWriteCount[v], i)
						}
					}
				case history.MicroRead:
					if st != history.OK {
						continue
					}
					seen := make(map[int64]bool, len(m.Values))
					for _, v := range m.Values {
						if seen[v] {
							recordViolation(AnomalySettlementIdempotency, []int{i},
								fmt.Sprintf("%s: duplicate settlement ID %d observed in %s read %s (%v).",
									AnomalySettlementIdempotency, v, m.Key, o.Describe(), m.Values))
						}
						seen[v] = true
						if isSourceLegKey(m.Key) {
							possibleLedger[v] = i
							if _, exists := committedLedger[v]; !exists {
								committedLedger[v] = i
							}
						} else if isDestLegKey(m.Key) {
							if _, exists := committedBroker[v]; !exists {
								committedBroker[v] = i
							}
						}
					}
				}
			}
			continue
		}

		// Single-key ops
		switch o.Input.F {
		case history.FWrite, history.FAdd, history.FEnqueue, "settle":
			if o.Input.Value == nil {
				continue
			}
			v := *o.Input.Value
			if isSourceLegKey(o.Input.Key) {
				possibleLedger[v] = i
				if st == history.OK {
					committedLedger[v] = i
					ledgerWriteCount[v] = append(ledgerWriteCount[v], i)
				}
			} else if isDestLegKey(o.Input.Key) {
				if st == history.OK {
					committedBroker[v] = i
					brokerWriteCount[v] = append(brokerWriteCount[v], i)
				}
			} else {
				possibleLedger[v] = i
				if st == history.OK {
					committedLedger[v] = i
				}
			}
		case history.FRead:
			if st != history.OK {
				continue
			}
			seen := make(map[int64]bool, len(o.Output.Values))
			for _, v := range o.Output.Values {
				if seen[v] {
					recordViolation(AnomalySettlementIdempotency, []int{i},
						fmt.Sprintf("%s: duplicate settlement ID %d observed in %s read %s.",
							AnomalySettlementIdempotency, v, o.Input.Key, o.Describe()))
				}
				seen[v] = true
			}
		}
	}

	// 1. Check idempotency across multiple committed writes of the same txID.
	for v, idxs := range ledgerWriteCount {
		if len(idxs) > 1 {
			recordViolation(AnomalySettlementIdempotency, []int{idxs[0], idxs[1]},
				fmt.Sprintf("%s: settlement ID %d was committed %d times to source ledger (%s and %s).",
					AnomalySettlementIdempotency, v, len(idxs), ops[idxs[0]].Describe(), ops[idxs[1]].Describe()))
		}
	}
	for v, idxs := range brokerWriteCount {
		if len(idxs) > 1 {
			recordViolation(AnomalySettlementIdempotency, []int{idxs[0], idxs[1]},
				fmt.Sprintf("%s: settlement ID %d was published %d times to destination broker (%s and %s).",
					AnomalySettlementIdempotency, v, len(idxs), ops[idxs[0]].Describe(), ops[idxs[1]].Describe()))
		}
	}

	// 2. Check reconciliation reads and final state between ledger and broker.
	var latestBrokerReadIdx = -1
	var latestLedgerSet map[int64]bool
	var latestBrokerSet map[int64]bool

	for i, o := range ops {
		if o.Status() != history.OK {
			continue
		}
		var (
			hasL, hasB bool
			lVals      []int64
			bVals      []int64
		)
		micro := o.Output.Txn
		for _, m := range micro {
			if m.F != history.MicroRead {
				continue
			}
			if isSourceLegKey(m.Key) {
				hasL = true
				lVals = m.Values
			} else if isDestLegKey(m.Key) {
				hasB = true
				bVals = m.Values
			}
		}
		if o.Input.F == history.FRead {
			if isSourceLegKey(o.Input.Key) {
				hasL = true
				lVals = o.Output.Values
			} else if isDestLegKey(o.Input.Key) {
				hasB = true
				bVals = o.Output.Values
			}
		}
		if hasL {
			latestLedgerSet = make(map[int64]bool, len(lVals))
			for _, v := range lVals {
				latestLedgerSet[v] = true
			}
		}
		if hasB {
			latestBrokerReadIdx = i
			latestBrokerSet = make(map[int64]bool, len(bVals))
			for _, v := range bVals {
				latestBrokerSet[v] = true
			}
		}

		// If a single atomic audit read observed both legs, check that any
		// settlement that completed strictly before this audit began is in
		// both legs, and no orphan credit exists in broker without ledger.
		if hasL && hasB {
			lSet := make(map[int64]bool, len(lVals))
			for _, v := range lVals {
				lSet[v] = true
			}
			bSet := make(map[int64]bool, len(bVals))
			for _, v := range bVals {
				bSet[v] = true
			}
			for _, v := range bVals {
				if !lSet[v] {
					if _, poss := possibleLedger[v]; !poss {
						recordViolation(AnomalySettlementOrphan, []int{i},
							fmt.Sprintf("%s: destination clearing log contains orphan settlement ID %d in %s, which was never committed to the source ledger.",
								AnomalySettlementOrphan, v, o.Describe()))
					} else if wIdx, ok := committedLedger[v]; !ok || ops[wIdx].Call > o.Return {
						recordViolation(AnomalySettlementOrphan, []int{i},
							fmt.Sprintf("%s: destination clearing log contains settlement ID %d in %s without a backing source ledger entry (ledger=%v, broker=%v).",
								AnomalySettlementOrphan, v, o.Describe(), lVals, bVals))
					}
				}
			}
			for _, v := range lVals {
				if !bSet[v] {
					if wIdx, ok := committedLedger[v]; ok && ops[wIdx].Return < o.Call {
						w := []int{wIdx, i}
						sort.Ints(w)
						recordViolation(AnomalySettlementAtomicity, w,
							fmt.Sprintf("%s: settlement ID %d committed in %s at %s is present in source ledger but missing from destination clearing log in %s at %s (dropped outbox event).",
								AnomalySettlementAtomicity, v, ops[wIdx].Describe(), fmtNs(ops[wIdx].Return), o.Describe(), fmtNs(o.Call)))
					}
				}
			}
		}
	}

	// Also check latest broker read against all committed ledger settlements that finished before it.
	if latestBrokerReadIdx >= 0 {
		bOp := ops[latestBrokerReadIdx]
		var txIDs []int64
		for v := range committedLedger {
			txIDs = append(txIDs, v)
		}
		sort.Slice(txIDs, func(a, b int) bool { return txIDs[a] < txIDs[b] })
		for _, v := range txIDs {
			wIdx := committedLedger[v]
			if ops[wIdx].Return < bOp.Call && !latestBrokerSet[v] {
				w := []int{wIdx, latestBrokerReadIdx}
				sort.Ints(w)
				recordViolation(AnomalySettlementAtomicity, w,
					fmt.Sprintf("%s: settlement ID %d committed in %s at %s is missing from destination clearing read %s at %s.",
						AnomalySettlementAtomicity, v, ops[wIdx].Describe(), fmtNs(ops[wIdx].Return), bOp.Describe(), fmtNs(bOp.Call)))
			}
		}
		for v := range latestBrokerSet {
			if _, ok := possibleLedger[v]; !ok && (latestLedgerSet == nil || !latestLedgerSet[v]) {
				recordViolation(AnomalySettlementOrphan, []int{latestBrokerReadIdx},
					fmt.Sprintf("%s: destination clearing read %s observed orphan settlement ID %d that was never committed to the source ledger.",
						AnomalySettlementOrphan, bOp.Describe(), v))
			}
		}
	}

	if len(kinds) > 0 {
		sort.Strings(kinds)
		return true, witness, diagnosis, kinds
	}
	return false, nil, "", nil
}

// Diagnose implements Diagnoser for Settlement.
func (s Settlement) Diagnose(ops []history.Operation) ([]int, string, bool) {
	v, w, d, _ := s.Decide(ops)
	return w, d, v
}
