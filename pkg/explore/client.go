package explore

import (
	"fmt"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/internal/rng"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/sim"
	"github.com/kemalhafizh/strife/pkg/workload"
)

// client is a simulated workload process. Like a Jepsen client it is bound to
// a home node: it follows leader redirects (which guarantee the op was not
// applied), and after a timeout it records "info", reconnects to its home
// node and continues under a fresh process id.
type client struct {
	w       *sim.World
	id      sim.NodeID
	idx     int
	cfg     Config
	sys     System
	rand    *rng.Rand
	rec     *history.Recorder
	process int
	guess   sim.NodeID
	plugin  *workload.Plugin
	step    int

	busy    bool
	reqID   uint64
	gen     uint64
	op      sim.KVOp
	invoked history.Op
}

func (c *client) Start() {
	c.w.After(c.id, time.Duration(c.rand.Int64n(int64(10*time.Millisecond))), c.next)
}

func (c *client) next() {
	if c.busy || c.w.Now() >= int64(c.cfg.Duration) {
		return
	}
	var inv history.Op
	if c.plugin != nil {
		genOp, err := c.plugin.NextOp(c.process, c.step, c.cfg.Keys, c.cfg.Values, c.rand)
		c.step++
		if err != nil {
			return
		}
		inv = genOp
		if !strings.HasPrefix(inv.Key, "k") {
			inv.Key = fmt.Sprintf("k%d", c.step%c.cfg.Keys)
		}
		switch inv.F {
		case history.FRead:
			c.op = sim.KVOp{F: history.FRead, Key: inv.Key}
		case history.FCas:
			var oldV, newV int64
			if inv.Old != nil {
				oldV = *inv.Old
			}
			if inv.Value != nil {
				newV = *inv.Value
			}
			c.op = sim.KVOp{F: history.FCas, Key: inv.Key, Old: oldV, Value: newV}
			inv.Old, inv.Value = history.Int(oldV), history.Int(newV)
		default:
			var v int64
			if inv.Value != nil {
				v = *inv.Value
			}
			inv.F = history.FWrite
			inv.Value = history.Int(v)
			inv.Old = nil
			inv.Txn = nil
			c.op = sim.KVOp{F: history.FWrite, Key: inv.Key, Value: v}
		}
	} else if c.sys.Raft.Ledger {
		nAccts := max(2, c.cfg.Keys)
		r := c.rand.Float64()
		if r < 0.2 {
			c.op = sim.KVOp{F: sim.FLedgerAudit, Key: "audit", Value: int64(nAccts)}
			auditTxn := make([]history.MicroOp, nAccts)
			for i := 0; i < nAccts; i++ {
				auditTxn[i] = history.MicroOp{F: history.MicroRead, Key: fmt.Sprintf("acct:%d", i)}
			}
			inv = history.Op{Process: c.process, Type: history.Invoke, F: history.FTxn, Key: "audit", Txn: auditTxn}
		} else {
			fromIdx := c.rand.Intn(nAccts)
			toIdx := (fromIdx + 1 + c.rand.Intn(nAccts-1)) % nAccts
			fromAcct := fmt.Sprintf("acct:%d", fromIdx)
			toAcct := fmt.Sprintf("acct:%d", toIdx)
			pairKey := fromAcct + "->" + toAcct
			amt := (1 + c.rand.Int64n(int64(c.cfg.Values))) * 200
			c.op = sim.KVOp{F: sim.FLedgerTransfer, Key: pairKey, Value: amt}
			inv = history.Op{
				Process: c.process,
				Type:    history.Invoke,
				F:       history.FTxn,
				Key:     pairKey,
				Value:   history.Int(amt),
				Txn: []history.MicroOp{
					{F: history.MicroRead, Key: fromAcct},
					{F: history.MicroWrite, Key: fromAcct},
					{F: history.MicroRead, Key: toAcct},
					{F: history.MicroWrite, Key: toAcct},
				},
			}
		}
	} else if c.sys.Raft.Settlement {
		c.step++
		if c.rand.Float64() < 0.25 {
			c.op = sim.KVOp{F: sim.FSettlementAudit, Key: "settlement"}
			inv = history.Op{
				Process: c.process,
				Type:    history.Invoke,
				F:       history.FTxn,
				Key:     "settlement",
				Txn: []history.MicroOp{
					{F: history.MicroRead, Key: "ledger"},
					{F: history.MicroRead, Key: "broker"},
				},
			}
		} else {
			txID := int64(c.idx+1)*1_000_000 + int64(c.step)
			c.op = sim.KVOp{F: sim.FSettlementWrite, Key: "settlement", Value: txID}
			inv = history.Op{
				Process: c.process,
				Type:    history.Invoke,
				F:       history.FTxn,
				Key:     "settlement",
				Value:   history.Int(txID),
				Txn: []history.MicroOp{
					{F: history.MicroAppend, Key: "ledger", Value: history.Int(txID)},
					{F: history.MicroAppend, Key: "broker", Value: history.Int(txID)},
				},
			}
		}
	} else {
		key := fmt.Sprintf("k%d", c.rand.Intn(c.cfg.Keys))
		inv = history.Op{Process: c.process, Type: history.Invoke, Key: key}
		r := c.rand.Float64()
		switch {
		case r < c.cfg.ReadRatio:
			c.op = sim.KVOp{F: history.FRead, Key: key}
			inv.F = history.FRead
		case r < c.cfg.ReadRatio+c.cfg.CasRatio:
			old, nw := c.rand.Int64n(int64(c.cfg.Values)), c.rand.Int64n(int64(c.cfg.Values))
			c.op = sim.KVOp{F: history.FCas, Key: key, Old: old, Value: nw}
			inv.F, inv.Old, inv.Value = history.FCas, history.Int(old), history.Int(nw)
		default:
			v := c.rand.Int64n(int64(c.cfg.Values))
			c.op = sim.KVOp{F: history.FWrite, Key: key, Value: v}
			inv.F, inv.Value = history.FWrite, history.Int(v)
		}
	}
	c.busy = true
	c.invoked = c.rec.Record(inv)
	c.gen++
	gen := c.gen
	c.w.After(c.id, c.cfg.OpTimeout, func() {
		if c.busy && c.gen == gen {
			c.guess = c.home()
			if c.sys.Raft.Ledger {
				c.completeLedger(history.Info, sim.LedgerReply{})
			} else if c.sys.Raft.Settlement {
				c.completeSettlement(history.Info, sim.SettlementReply{})
			} else {
				c.complete(history.Info, sim.ClientReply{})
			}
		}
	})
	c.attempt()
}

func (c *client) home() sim.NodeID { return sim.NodeID(c.idx % c.cfg.Nodes) }

func (c *client) attempt() {
	c.reqID++
	c.w.Send(c.id, c.guess, sim.ClientRequest{ReqID: c.reqID, Op: c.op})
}

func (c *client) Receive(from sim.NodeID, msg any) {
	if sr, ok := msg.(sim.SettlementReply); ok {
		if !c.busy || sr.ReqID != c.reqID {
			return
		}
		switch sr.Status {
		case sim.StatusRedirect:
			if sr.Leader >= 0 && sr.Leader != from {
				c.guess = sr.Leader
			} else {
				c.guess = sim.NodeID((int(from) + 1) % c.cfg.Nodes)
			}
			gen := c.gen
			c.w.After(c.id, 5*time.Millisecond, func() {
				if c.busy && c.gen == gen {
					c.attempt()
				}
			})
		case sim.StatusOK:
			c.completeSettlement(history.OK, sr)
		case sim.StatusFail:
			c.completeSettlement(history.Fail, sr)
		}
		return
	}
	if lr, ok := msg.(sim.LedgerReply); ok {
		if !c.busy || lr.ReqID != c.reqID {
			return
		}
		switch lr.Status {
		case sim.StatusRedirect:
			if lr.Leader >= 0 && lr.Leader != from {
				c.guess = lr.Leader
			} else {
				c.guess = sim.NodeID((int(from) + 1) % c.cfg.Nodes)
			}
			gen := c.gen
			c.w.After(c.id, 5*time.Millisecond, func() {
				if c.busy && c.gen == gen {
					c.attempt()
				}
			})
		case sim.StatusOK:
			c.completeLedger(history.OK, lr)
		case sim.StatusFail:
			c.completeLedger(history.Fail, lr)
		}
		return
	}
	m, ok := msg.(sim.ClientReply)
	if !ok || !c.busy || m.ReqID != c.reqID {
		return
	}
	switch m.Status {
	case sim.StatusRedirect:
		if m.Leader >= 0 && m.Leader != from {
			c.guess = m.Leader
		} else {
			c.guess = sim.NodeID((int(from) + 1) % c.cfg.Nodes)
		}
		gen := c.gen
		c.w.After(c.id, 5*time.Millisecond, func() {
			if c.busy && c.gen == gen {
				c.attempt()
			}
		})
	case sim.StatusOK:
		c.complete(history.OK, m)
	case sim.StatusFail:
		c.complete(history.Fail, m)
	}
}

func (c *client) completeSettlement(t history.Type, sr sim.SettlementReply) {
	out := history.Op{Process: c.process, Type: t, F: history.FTxn, Key: c.invoked.Key, Value: c.invoked.Value}
	if t == history.OK {
		switch c.op.F {
		case sim.FSettlementWrite:
			out.Txn = []history.MicroOp{
				{F: history.MicroAppend, Key: "ledger", Value: history.Int(sr.TxID)},
				{F: history.MicroAppend, Key: "broker", Value: history.Int(sr.TxID)},
			}
		case sim.FSettlementAudit:
			out.Txn = []history.MicroOp{
				{F: history.MicroRead, Key: "ledger", Values: append([]int64(nil), sr.Ledger...)},
				{F: history.MicroRead, Key: "broker", Values: append([]int64(nil), sr.Broker...)},
			}
		}
	}
	if t == history.Info {
		out.Error = "timeout"
	}
	if t != history.Info {
		out.Node = fmt.Sprintf("n%d", sr.Leader)
	}
	c.rec.Record(out)
	c.busy = false
	c.gen++
	if t == history.Info {
		c.process += c.cfg.Clients
	}
	think := time.Duration(0)
	if c.cfg.Think > 0 {
		think = time.Duration(c.rand.Int64n(int64(c.cfg.Think)))
	}
	c.w.After(c.id, think, c.next)
}

func (c *client) completeLedger(t history.Type, lr sim.LedgerReply) {
	out := history.Op{Process: c.process, Type: t, F: history.FTxn, Key: c.invoked.Key}
	if t == history.OK {
		switch c.op.F {
		case sim.FLedgerTransfer:
			fromAcct, toAcct, _ := strings.Cut(c.op.Key, "->")
			out.Txn = []history.MicroOp{
				{F: history.MicroRead, Key: fromAcct, Value: history.Int(lr.OldFrom)},
				{F: history.MicroWrite, Key: fromAcct, Value: history.Int(lr.NewFrom)},
				{F: history.MicroRead, Key: toAcct, Value: history.Int(lr.OldTo)},
				{F: history.MicroWrite, Key: toAcct, Value: history.Int(lr.NewTo)},
			}
		case sim.FLedgerAudit:
			out.Txn = make([]history.MicroOp, len(lr.Balances))
			for i, b := range lr.Balances {
				out.Txn[i] = history.MicroOp{F: history.MicroRead, Key: fmt.Sprintf("acct:%d", i), Value: history.Int(b)}
			}
		}
	}
	if t == history.Info {
		out.Error = "timeout"
	}
	if t != history.Info {
		out.Node = fmt.Sprintf("n%d", lr.Leader)
	}
	c.rec.Record(out)
	c.busy = false
	c.gen++
	if t == history.Info {
		c.process += c.cfg.Clients
	}
	think := time.Duration(0)
	if c.cfg.Think > 0 {
		think = time.Duration(c.rand.Int64n(int64(c.cfg.Think)))
	}
	c.w.After(c.id, think, c.next)
}

func (c *client) complete(t history.Type, m sim.ClientReply) {
	out := history.Op{Process: c.process, Type: t, F: c.invoked.F, Key: c.invoked.Key, Value: c.invoked.Value, Old: c.invoked.Old}
	if t == history.OK && c.op.F == history.FRead {
		out.Value = nil
		if m.Found {
			out.Value = history.Int(m.Value)
		}
	}
	if t == history.Info {
		out.Value, out.Old = nil, nil
		out.Error = "timeout"
	}
	if t != history.Info {
		out.Node = fmt.Sprintf("n%d", m.Leader)
	}
	c.rec.Record(out)
	c.busy = false
	c.gen++
	if t == history.Info {
		c.process += c.cfg.Clients
	}
	think := time.Duration(0)
	if c.cfg.Think > 0 {
		think = time.Duration(c.rand.Int64n(int64(c.cfg.Think)))
	}
	c.w.After(c.id, think, c.next)
}
