package model

import (
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"strings"

	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/txn"
)

// Financial ledger anomaly identifiers reported by Ledger.Decide.
const (
	AnomalyLedgerConservation = "LEDGER-CONSERVATION"
	AnomalyLedgerOverdraft    = "LEDGER-OVERDRAFT"
	AnomalyLedgerDoubleSpend  = "LEDGER-DOUBLE-SPEND"
	AnomalyLedgerUnbalanced   = "LEDGER-UNBALANCED"
)

// DefaultLedgerInitialBalance is the default opening balance per account when
// not overridden on the Ledger model or by an explicit init transaction.
const DefaultLedgerInitialBalance int64 = 1000

// Ledger models a multi-account double-entry financial ledger.
//
// Every transfer transaction reads the source and destination accounts and
// writes their updated balances such that:
//  1. Journal Entry Integrity (LEDGER-UNBALANCED): the sum of balance deltas
//     across all legs of a transfer equals zero (\sum \Delta_i = 0).
//  2. Non-Negative Balance / No Overdraft (LEDGER-OVERDRAFT): no committed
//     transfer leaves an account with a balance < 0, and concurrent transfers
//     may not overdraft an account via write skew (G2-item).
//  3. Conservation of Money (LEDGER-CONSERVATION): total money supply across
//     all accounts remains invariant (\sum B_i = M_0) across all audits and
//     committed state transitions.
//  4. Idempotency / No Double-Spend (LEDGER-DOUBLE-SPEND): an indeterminate
//     ("info") transfer and its committed ("ok") retry sharing the same
//     transaction identifier ("tx:<id>") may not both debit the account.
type Ledger struct {
	InitialBalance int64  // default per-account opening balance (0 => infer or 1000)
	Level          string // target transactional isolation level, default "serializable"
}

type ledgerState struct {
	accounts map[string]int64
	seenTx   map[string]bool
	supply   int64
	inited   bool
}

func (s ledgerState) keys() []string {
	ks := make([]string, 0, len(s.accounts))
	for k := range s.accounts {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func (s ledgerState) Hash() uint64 {
	h := fnv.New64a()
	for _, k := range s.keys() {
		_, _ = fmt.Fprintf(h, "%s:%d;", k, s.accounts[k])
	}
	var txKeys []string
	for k := range s.seenTx {
		txKeys = append(txKeys, k)
	}
	sort.Strings(txKeys)
	for _, k := range txKeys {
		_, _ = fmt.Fprintf(h, "tx=%s;", k)
	}
	return h.Sum64()
}

func (s ledgerState) Equal(o State) bool {
	t, ok := o.(ledgerState)
	if !ok || len(s.accounts) != len(t.accounts) || len(s.seenTx) != len(t.seenTx) {
		return false
	}
	for k, v := range s.accounts {
		if vb, exists := t.accounts[k]; !exists || vb != v {
			return false
		}
	}
	for k := range s.seenTx {
		if !t.seenTx[k] {
			return false
		}
	}
	return true
}

func (s ledgerState) String() string {
	var b strings.Builder
	var sum int64
	for i, k := range s.keys() {
		if i > 0 {
			b.WriteString(" ")
		}
		v := s.accounts[k]
		sum += v
		fmt.Fprintf(&b, "%s=%d", k, v)
	}
	if len(s.accounts) > 0 {
		fmt.Fprintf(&b, " (supply=%d)", sum)
	}
	return b.String()
}

func (m Ledger) initBal() int64 {
	if m.InitialBalance > 0 {
		return m.InitialBalance
	}
	return DefaultLedgerInitialBalance
}

// Name implements Model.
func (Ledger) Name() string { return "ledger" }

// Init implements Model.
func (Ledger) Init() State {
	return ledgerState{
		accounts: map[string]int64{},
		seenTx:   map[string]bool{},
	}
}

// Relevant implements Model.
func (Ledger) Relevant(o history.Operation) bool {
	return o.Status() != history.Fail
}

// Partition implements Model: double-entry transfers and audits span accounts,
// so the entire ledger history is checked as a single partition.
func (Ledger) Partition(ops []history.Operation) ([]string, [][]history.Operation) {
	return []string{"ledger"}, [][]history.Operation{ops}
}

func isTxKey(k string) bool {
	return strings.HasPrefix(k, "tx:") || strings.HasPrefix(k, "idemp:")
}

// Step implements Model for strict-serializable sequential execution.
func (m Ledger) Step(s State, in, out history.Op) (bool, State) {
	st := s.(ledgerState)
	if out.Type == history.Fail {
		return true, st
	}
	if in.F != history.FTxn {
		return true, st
	}
	micro := in.Txn
	if out.Type == history.OK && len(out.Txn) > 0 {
		micro = out.Txn
	}
	if len(micro) == 0 {
		return true, st
	}

	next := ledgerState{
		accounts: make(map[string]int64, len(st.accounts)),
		seenTx:   make(map[string]bool, len(st.seenTx)),
		supply:   st.supply,
		inited:   st.inited,
	}
	for k, v := range st.accounts {
		next.accounts[k] = v
	}
	for k, v := range st.seenTx {
		next.seenTx[k] = v
	}

	getBal := func(acct string) int64 {
		if v, ok := next.accounts[acct]; ok {
			return v
		}
		b := m.initBal()
		next.accounts[acct] = b
		next.supply += b
		return b
	}

	// Explicit init transaction: write-only setup with Key == "init".
	if in.Key == "init" {
		for _, op := range micro {
			if (op.F == history.MicroWrite || op.F == "write") && op.Value != nil {
				old, existed := next.accounts[op.Key]
				if existed {
					next.supply -= old
				}
				next.accounts[op.Key] = *op.Value
				next.supply += *op.Value
			}
		}
		next.inited = true
		return true, next
	}

	// Idempotency check: if a transaction with the same tx key already took
	// effect, an info retry may be a no-op, but an ok retry that mutates to
	// a new balance is a double-spend.
	if isTxKey(in.Key) && next.seenTx[in.Key] {
		if out.Type == history.Info {
			return true, st
		}
		return false, st
	}

	var reads, writes int
	var netDelta int64
	var auditSum int64
	readSeen := map[string]bool{}

	for _, op := range micro {
		switch op.F {
		case history.MicroRead, "read":
			reads++
			cur := getBal(op.Key)
			if out.Type == history.OK && op.Value != nil && *op.Value != cur {
				return false, st
			}
			if !readSeen[op.Key] {
				readSeen[op.Key] = true
				auditSum += cur
			}
		case history.MicroWrite, "write":
			writes++
			if op.Value == nil {
				return false, st
			}
			cur := getBal(op.Key)
			newVal := *op.Value
			if newVal < 0 {
				// Overdraft: negative balance is never allowed.
				return false, st
			}
			delta := newVal - cur
			netDelta += delta
			next.accounts[op.Key] = newVal
			next.supply += delta
		}
	}

	if writes > 0 {
		// Double-entry conservation law: net delta across all legs must be 0.
		if netDelta != 0 {
			return false, st
		}
		if isTxKey(in.Key) {
			next.seenTx[in.Key] = true
		}
	} else if reads >= 2 && len(readSeen) == len(next.accounts) && out.Type == history.OK {
		if auditSum != next.supply {
			return false, st
		}
	}

	return true, next
}

type ledgerFinding struct {
	kind        string
	witness     []int
	explanation string
}

// Decide implements Decider, checking all four financial ledger invariants in
// polynomial time alongside Elle/Adya transactional dependency analysis.
func (m Ledger) Decide(ops []history.Operation) (bool, []int, string, []string) {
	var findings []ledgerFinding
	addFinding := func(kind string, witness []int, explanation string) {
		dedup := make([]int, 0, len(witness))
		for _, idx := range witness {
			if idx >= 0 && idx < len(ops) && !slices.Contains(dedup, idx) {
				dedup = append(dedup, idx)
			}
		}
		sort.Ints(dedup)
		findings = append(findings, ledgerFinding{
			kind:        kind,
			witness:     dedup,
			explanation: explanation,
		})
	}

	// 1. Discover all accounts and their opening balances B_a(0).
	accounts := map[string]int64{}
	explicitInit := map[string]bool{}
	writtenValues := map[string]map[int64][]int{} // acct -> balance -> op indices that wrote it

	for i, o := range ops {
		if o.Status() == history.Fail {
			continue
		}
		micro := effectiveMicroOps(o)
		if o.Input.Key == "init" && o.Status() == history.OK {
			for _, mop := range micro {
				if isWriteMicro(mop.F) && mop.Value != nil {
					accounts[mop.Key] = *mop.Value
					explicitInit[mop.Key] = true
				}
			}
			continue
		}
		for _, mop := range micro {
			if mop.Key == "" {
				continue
			}
			if _, ok := accounts[mop.Key]; !ok {
				accounts[mop.Key] = m.initBal()
			}
			if isWriteMicro(mop.F) && mop.Value != nil && o.Status() == history.OK {
				if writtenValues[mop.Key] == nil {
					writtenValues[mop.Key] = map[int64][]int{}
				}
				writtenValues[mop.Key][*mop.Value] = append(writtenValues[mop.Key][*mop.Value], i)
			}
		}
	}

	// If an account was read with a value that was never produced by any write
	// and InitialBalance was not explicitly fixed, infer B_a(0) from that root
	// read value so custom opening balances work seamlessly.
	if m.InitialBalance <= 0 {
		for _, o := range ops {
			if o.Status() != history.OK {
				continue
			}
			for _, mop := range effectiveMicroOps(o) {
				if isReadMicro(mop.F) && mop.Value != nil && !explicitInit[mop.Key] {
					v := *mop.Value
					if len(writtenValues[mop.Key][v]) == 0 && v >= 0 {
						accounts[mop.Key] = v
						explicitInit[mop.Key] = true
					}
				}
			}
		}
	}

	var totalInitialSupply int64
	for _, bal := range accounts {
		totalInitialSupply += bal
	}

	// Track per-(account, balance) creation count vs consumption by mutating
	// transactions, to detect concurrent overdraft write skew (G2-item) and
	// lost updates (P4).
	type acctVal struct {
		acct string
		val  int64
	}
	type debitConsumer struct {
		opIdx int
		delta int64 // newVal - oldVal (negative for debit)
	}
	consumers := map[acctVal][]debitConsumer{}
	creations := map[acctVal]int{}
	infoWrites := map[string]int{}
	for acct, initB := range accounts {
		creations[acctVal{acct: acct, val: initB}] = 1
	}

	// Track idempotency keys across info and ok operations.
	type txAttempt struct {
		opIdx  int
		status history.Type
		from   string
		to     string
		amount int64
	}
	txByKey := map[string][]txAttempt{}

	for i, o := range ops {
		if o.Status() == history.Fail || o.Input.Key == "init" {
			continue
		}
		micro := effectiveMicroOps(o)
		if len(micro) == 0 {
			continue
		}

		readBal := map[string]int64{}
		writeBal := map[string]int64{}
		var writeOrder []string
		var readOrder []string

		for _, mop := range micro {
			if isReadMicro(mop.F) && mop.Value != nil {
				if _, seen := readBal[mop.Key]; !seen {
					readBal[mop.Key] = *mop.Value
					readOrder = append(readOrder, mop.Key)
				}
				if o.Status() == history.OK && *mop.Value < 0 {
					writers := writtenValues[mop.Key][*mop.Value]
					wit := append([]int(nil), writers...)
					wit = append(wit, i)
					addFinding(
						AnomalyLedgerOverdraft,
						wit,
						fmt.Sprintf("Negative account balance observed by %s: account %q has balance %d (< 0).",
							o.Describe(), mop.Key, *mop.Value),
					)
				}
			} else if isWriteMicro(mop.F) && mop.Value != nil {
				if _, seen := writeBal[mop.Key]; !seen {
					writeOrder = append(writeOrder, mop.Key)
				}
				writeBal[mop.Key] = *mop.Value
			} else if isWriteMicro(mop.F) && mop.Value == nil && o.Status() == history.Info {
				infoWrites[mop.Key]++
			}
		}

		// Record non-trivial balance creations (including Info ops, which may
		// have taken effect).
		for _, acct := range writeOrder {
			newV := writeBal[acct]
			if oldV, hasRead := readBal[acct]; !hasRead || oldV != newV {
				creations[acctVal{acct: acct, val: newV}]++
			}
		}

		// Track idempotency attempts for "tx:<id>" keys.
		if isTxKey(o.Input.Key) && len(writeOrder) > 0 {
			var fromAcct, toAcct string
			var amt int64
			for _, acct := range writeOrder {
				if oldV, ok := readBal[acct]; ok {
					d := writeBal[acct] - oldV
					if d < 0 {
						fromAcct = acct
						amt = -d
					} else if d > 0 {
						toAcct = acct
					}
				}
			}
			txByKey[o.Input.Key] = append(txByKey[o.Input.Key], txAttempt{
				opIdx:  i,
				status: o.Status(),
				from:   fromAcct,
				to:     toAcct,
				amount: amt,
			})
		}

		if o.Status() != history.OK {
			continue
		}

		// Check 1 & 2a: Transfer transactions (writes > 0).
		if len(writeOrder) > 0 {
			for _, acct := range writeOrder {
				newV := writeBal[acct]
				if newV < 0 {
					var wit []int
					if oldV, hasR := readBal[acct]; hasR {
						for _, c := range consumers[acctVal{acct: acct, val: oldV}] {
							wit = append(wit, c.opIdx)
						}
					}
					wit = append(wit, i)
					addFinding(
						AnomalyLedgerOverdraft,
						wit,
						fmt.Sprintf("Overdraft violation in %s: account %q written with negative balance %d (< 0).",
							o.Describe(), acct, newV),
					)
				}
			}

			// Check balanced journal entry when pre-balances are present.
			var netDelta int64
			allHavePre := true
			for _, acct := range writeOrder {
				oldV, ok := readBal[acct]
				if !ok {
					allHavePre = false
					break
				}
				d := writeBal[acct] - oldV
				netDelta += d
				if d != 0 {
					av := acctVal{acct: acct, val: oldV}
					consumers[av] = append(consumers[av], debitConsumer{opIdx: i, delta: d})
				}
			}
			if allHavePre && netDelta != 0 {
				addFinding(
					AnomalyLedgerUnbalanced,
					[]int{i},
					fmt.Sprintf("Unbalanced double-entry journal in %s: sum of leg deltas across %v is %+d (must equal 0).",
						o.Describe(), writeOrder, netDelta),
				)
			} else if !allHavePre && len(writeOrder) == 1 {
				addFinding(
					AnomalyLedgerUnbalanced,
					[]int{i},
					fmt.Sprintf("Single-legged mutation in %s: account %q written to %d without a balancing counter-entry.",
						o.Describe(), writeOrder[0], writeBal[writeOrder[0]]),
				)
			}
		}

		// Check 4: Multi-account Audit Read Conservation (\sum B_i == M_0).
		if len(writeOrder) == 0 && len(readOrder) >= 2 && (len(readOrder) == len(accounts) || o.Input.Key == "audit") {
			var observedSum int64
			var expectedSubSum int64
			for _, acct := range readOrder {
				observedSum += readBal[acct]
				expectedSubSum += accounts[acct]
			}
			targetSupply := totalInitialSupply
			if len(readOrder) < len(accounts) {
				targetSupply = expectedSubSum
			}
			if observedSum != targetSupply {
				wit := []int{i}
				// Include the most recent conflicting transfer in the witness.
				for _, acct := range readOrder {
					v := readBal[acct]
					if ws := writtenValues[acct][v]; len(ws) > 0 {
						wit = append(wit, ws[len(ws)-1])
					}
				}
				addFinding(
					AnomalyLedgerConservation,
					wit,
					fmt.Sprintf("Conservation of money violated in audit %s: observed sum across %d accounts is %d, expected total supply %d (discrepancy %+d).",
						o.Describe(), len(readOrder), observedSum, targetSupply, observedSum-targetSupply),
				)
			}
		}
	}

	// Check 2b: Concurrent Overdraft Write Skew (G2-item) & Lost Updates on (acct, val).
	var sortedAVs []acctVal
	for av := range consumers {
		sortedAVs = append(sortedAVs, av)
	}
	sort.Slice(sortedAVs, func(i, j int) bool {
		if sortedAVs[i].acct != sortedAVs[j].acct {
			return sortedAVs[i].acct < sortedAVs[j].acct
		}
		return sortedAVs[i].val < sortedAVs[j].val
	})
	for _, av := range sortedAVs {
		cs := consumers[av]
		allowed := creations[av]
		deficit := len(cs) - allowed
		if deficit <= 0 {
			continue
		}
		if infoWrites[av.acct] > 0 {
			used := min(deficit, infoWrites[av.acct])
			infoWrites[av.acct] -= used
			allowed += used
			deficit -= used
		}
		if deficit <= 0 {
			continue
		}
		// More mutating transactions consumed state (acct, val) than the number
		// of times (acct, val) was ever created!
		var totalDebit int64
		var wit []int
		for _, c := range cs {
			wit = append(wit, c.opIdx)
			if c.delta < 0 {
				totalDebit += -c.delta
			}
		}
		if totalDebit > av.val {
			addFinding(
				AnomalyLedgerOverdraft,
				wit,
				fmt.Sprintf("Concurrent overdraft write skew (G2-item) on account %q: %d concurrent transfers read balance %d and debited %d total (exceeding balance by %d; true balance %d < 0).",
					av.acct, len(cs), av.val, totalDebit, totalDebit-av.val, av.val-totalDebit),
			)
		}
		addFinding(
			AnomalyLedgerConservation,
			wit,
			fmt.Sprintf("Lost update on account %q at balance %d: %d concurrent transfers mutated the same balance state (created %d time(s)), violating conservation of money.",
				av.acct, av.val, len(cs), allowed),
		)
	}

	// Check 3: Double-Spend across indeterminate ("info") retries.
	var txKeys []string
	for k := range txByKey {
		txKeys = append(txKeys, k)
	}
	sort.Strings(txKeys)
	for _, k := range txKeys {
		attempts := txByKey[k]
		if len(attempts) < 2 {
			continue
		}
		var infoAttempts, okAttempts []txAttempt
		for _, a := range attempts {
			switch a.status {
			case history.Info:
				infoAttempts = append(infoAttempts, a)
			case history.OK:
				okAttempts = append(okAttempts, a)
			}
		}
		if len(okAttempts) >= 2 {
			addFinding(
				AnomalyLedgerDoubleSpend,
				[]int{okAttempts[0].opIdx, okAttempts[1].opIdx},
				fmt.Sprintf("Duplicate committed execution of idempotent transfer %q: both %s and %s committed.",
					k, ops[okAttempts[0].opIdx].Describe(), ops[okAttempts[1].opIdx].Describe()),
			)
			continue
		}
		if len(infoAttempts) > 0 && len(okAttempts) == 1 {
			okA := okAttempts[0]
			infoA := infoAttempts[0]
			okOp := ops[okA.opIdx]
			// Check whether okA observed a balance on fromAcct that already
			// deducted infoA's transfer when no other transfer accounted for it.
			for _, mop := range effectiveMicroOps(okOp) {
				if isReadMicro(mop.F) && mop.Key == okA.from && mop.Value != nil {
					readFrom := *mop.Value
					// If nobody else wrote readFrom except an unrecorded infoA
					// effect, or if the post-write balance of okA reflects 2x
					// deduction from the opening/prior balance:
					if len(writtenValues[okA.from][readFrom]) == 0 && readFrom != accounts[okA.from] {
						addFinding(
							AnomalyLedgerDoubleSpend,
							[]int{infoA.opIdx, okA.opIdx},
							fmt.Sprintf("Double-spend across timeout retry for %q: indeterminate attempt %s took effect before retry %s read %s=%d and deducted %d a second time.",
								k, ops[infoA.opIdx].Describe(), okOp.Describe(), okA.from, readFrom, okA.amount),
						)
					}
				}
			}
		}
	}

	// Check 5: Run Elle/Adya transactional cycle detection for underlying G0/G1/G2 cycles.
	level := txn.IsolationLevel(m.Level)
	if level == "" {
		level = txn.LevelSerializable
	}
	rep := txn.Check(txn.FromOperations(ops), level)
	if rep.Verdict == txn.Violation {
		idToIdx := make(map[int]int, len(ops))
		for i, o := range ops {
			idToIdx[o.ID] = i
		}
		for _, a := range rep.Anomalies {
			var wit []int
			if a.Cycle != nil {
				for _, id := range a.Cycle.Nodes {
					if idx, ok := idToIdx[id]; ok {
						wit = append(wit, idx)
					}
				}
			}
			for _, t := range a.Txns {
				if idx, ok := idToIdx[t.ID]; ok {
					wit = append(wit, idx)
				}
			}
			addFinding(string(a.Type), wit, fmt.Sprintf("[%s] %s", a.Type, a.Explanation))
		}
	}

	if len(findings) == 0 {
		return false, nil, "", nil
	}

	var kinds []string
	for _, f := range findings {
		if !slices.Contains(kinds, f.kind) {
			kinds = append(kinds, f.kind)
		}
	}
	first := findings[0]
	diag := fmt.Sprintf("[%s] %s", first.kind, first.explanation)
	if len(findings) > 1 {
		diag += fmt.Sprintf(" (+%d additional finding(s): %s)", len(findings)-1, strings.Join(kinds, ", "))
	}
	return true, first.witness, diag, kinds
}

func effectiveMicroOps(o history.Operation) []history.MicroOp {
	if o.Status() == history.OK && len(o.Output.Txn) > 0 {
		return o.Output.Txn
	}
	return o.Input.Txn
}

func isReadMicro(f string) bool {
	return f == history.MicroRead || f == "read"
}

func isWriteMicro(f string) bool {
	return f == history.MicroWrite || f == "write"
}
