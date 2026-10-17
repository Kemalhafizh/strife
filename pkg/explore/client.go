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
			c.complete(history.Info, sim.ClientReply{})
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
