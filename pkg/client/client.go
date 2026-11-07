// Package client talks to real databases on behalf of the workload and
// classifies every outcome as ok, fail (definitely not applied) or info
// (unknown), which is the single most important thing a Jepsen-style client
// must get right: calling an ambiguous failure "fail" produces false alarms,
// calling a definite failure "info" hides bugs.
package client

import (
	"context"
	"errors"
	"fmt"
	"github.com/kemalhafizh/strife/internal/secret"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
)

// Client executes operations against one node.
type Client interface {
	// Invoke executes op (an invoke event) and returns its completion with
	// Type set to ok, fail or info and, for reads, the observed value.
	Invoke(ctx context.Context, op history.Op) history.Op
	Close() error
}

// Factory opens a client connected to node index i.
type Factory func(i int) (Client, error)

// Config selects a database driver.
type Config struct {
	Kind string `json:"kind"` // etcd | redis | valkey | memory
	// Endpoints has one entry per node: http://10.0.0.1:2379 for etcd,
	// 10.0.0.1:6379 for redis/valkey.
	Endpoints []string `json:"endpoints,omitempty"`
	Prefix    string   `json:"prefix,omitempty"` // key prefix, default "strife/" or "strife:"
	// Credentials and transport security (postgres, mysql/tidb, redis).
	// Password accepts "env:NAME" or "file:PATH" so it never has to live in
	// strife.json, which is copied into every report.
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	Database string `json:"database,omitempty"`
	TLS      string `json:"tls,omitempty"`    // disable | require | verify-full
	TLSCA    string `json:"tls_ca,omitempty"` // PEM CA bundle for verify-full
	// InitSQL runs on every new SQL connection (postgres, mysql/tidb).
	InitSQL string `json:"init_sql,omitempty"`
	// Isolation is the transaction isolation level, set from
	// workload.isolation by the runner.
	Isolation string `json:"-"`
	// TxnIsolation overrides the SQL isolation transactions run at
	// ("read-committed", "repeatable-read", "serializable"), independently
	// of the level the history is checked against: e.g. read-only
	// transactions on a PostgreSQL hot standby cannot be serializable.
	TxnIsolation string `json:"txn_isolation,omitempty"`
}

// Redacted returns c with a literal password masked; secret references are
// kept, since they reveal only where the secret comes from.
func (c Config) Redacted() Config {
	c.Password = secret.Redact(c.Password)
	return c
}

// Kinds lists the supported drivers.
var Kinds = []string{"etcd", "redis", "valkey", "memory", "postgres", "kafka", "mysql", "tidb"}

// NewFactory builds a factory for the configured driver. Every key the test
// touches lives under prefix+namespace, so each run starts from empty
// registers no matter what earlier runs left behind (the checker assumes
// that every key starts absent). Memory clients all share one store.
func NewFactory(c Config, namespace string) (Factory, error) {
	sep := "/"
	if c.Kind == "redis" || c.Kind == "valkey" {
		sep = ":"
	}
	prefix := c.Prefix
	if prefix == "" {
		prefix = "strife" + sep
	}
	if namespace != "" {
		prefix += namespace + sep
	}
	switch c.Kind {
	case "etcd":
		if len(c.Endpoints) == 0 {
			return nil, fmt.Errorf("client: etcd needs db.endpoints")
		}
		return func(i int) (Client, error) {
			return NewEtcd(c.Endpoints[i%len(c.Endpoints)], prefix), nil
		}, nil
	case "redis", "valkey":
		if len(c.Endpoints) == 0 {
			return nil, fmt.Errorf("client: %s needs db.endpoints (host:port per node)", c.Kind)
		}
		opt, err := connOptions(c, "", "")
		if err != nil {
			return nil, err
		}
		return func(i int) (Client, error) {
			return NewRedisWith(c.Endpoints[i%len(c.Endpoints)], prefix, opt), nil
		}, nil
	case "postgres", "postgresql":
		if len(c.Endpoints) == 0 {
			return nil, fmt.Errorf("client: postgres needs db.endpoints (host:port per node)")
		}
		opt, err := connOptions(c, "postgres", "")
		if err != nil {
			return nil, err
		}
		return func(i int) (Client, error) {
			return NewPostgres(c.Endpoints[i%len(c.Endpoints)], prefix, opt), nil
		}, nil
	case "kafka":
		if len(c.Endpoints) == 0 {
			return nil, fmt.Errorf("client: kafka needs db.endpoints (host:port per node)")
		}
		return func(i int) (Client, error) {
			// Bootstrap from the client's own node first, then the others.
			n := len(c.Endpoints)
			boot := make([]string, n)
			for j := range boot {
				boot[j] = c.Endpoints[(i+j)%n]
			}
			return NewKafka(boot, prefix), nil
		}, nil
	case "mysql", "tidb":
		if len(c.Endpoints) == 0 {
			return nil, fmt.Errorf("client: %s needs db.endpoints (host:port per node)", c.Kind)
		}
		opt, err := connOptions(c, "root", "")
		if err != nil {
			return nil, err
		}
		return func(i int) (Client, error) {
			return NewMySQL(c.Endpoints[i%len(c.Endpoints)], prefix, opt), nil
		}, nil
	case "memory":
		store := NewMemoryStore()
		return func(i int) (Client, error) { return store.Client(), nil }, nil
	}
	return nil, fmt.Errorf("client: unknown db kind %q (%s)", c.Kind, strings.Join(Kinds, ", "))
}

// Classify maps a transport error to a completion type. Errors that prove
// the request never reached the server are definite failures; anything else
// on a mutating op is indeterminate. Reads never change state, so a failed
// read is always safe to call "fail".
func Classify(op history.Op, err error) history.Type {
	if err == nil {
		return history.OK
	}
	if readOnly(op) {
		return history.Fail
	}
	var ns *notSent
	if errors.As(err, &ns) {
		return history.Fail
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return history.Fail
	}
	return history.Info
}

func complete(op history.Op, t history.Type) history.Op {
	out := history.Op{Process: op.Process, Type: t, F: op.F, Key: op.Key, Value: op.Value, Old: op.Old}
	if op.F == history.FTxn && t == history.OK {
		// Callers fill in read results; appends are echoed.
		out.Txn = append([]history.MicroOp(nil), op.Txn...)
	}
	if op.F == history.FRead {
		out.Value = nil
	}
	return out
}

// DefaultLedgerInitialBalance is the opening balance assumed for any
// ledger account key that has not been written yet.
const DefaultLedgerInitialBalance int64 = 1000

// isLedgerTxn reports whether an FTxn operation belongs to the double-entry
// ledger workload ("audit" or "acct:i->acct:j" transfer) rather than the
// list-append workload.
func isLedgerTxn(op history.Op) bool {
	if op.Key == "audit" || strings.Contains(op.Key, "->") {
		return true
	}
	for _, m := range op.Txn {
		if m.F == history.MicroWrite {
			return true
		}
	}
	return false
}

// isSettlementTxn reports whether an FTxn operation belongs to the cross-system
// settlement / transactional-outbox workload ("settlement").
func isSettlementTxn(op history.Op) bool {
	return op.Key == "settlement"
}

// parseTransferAccounts extracts the source account, destination account,
// and positive transfer amount from a ledger transfer operation.
func parseTransferAccounts(op history.Op) (fromKey, toKey string, amt int64, ok bool) {
	if op.Value == nil || *op.Value <= 0 {
		return "", "", 0, false
	}
	amt = *op.Value
	if len(op.Txn) == 4 &&
		op.Txn[0].F == history.MicroRead && op.Txn[1].F == history.MicroWrite &&
		op.Txn[2].F == history.MicroRead && op.Txn[3].F == history.MicroWrite &&
		op.Txn[0].Key == op.Txn[1].Key && op.Txn[2].Key == op.Txn[3].Key {
		return op.Txn[0].Key, op.Txn[2].Key, amt, op.Txn[0].Key != "" && op.Txn[2].Key != ""
	}
	if from, to, found := strings.Cut(op.Key, "->"); found && from != "" && to != "" {
		return from, to, amt, true
	}
	return "", "", 0, false
}

// ---------------------------------------------------------------------------

// MemoryStore is a linearizable in-process store, the reference
// implementation used for self-tests and dry runs.
type MemoryStore struct {
	mu    sync.Mutex
	data  map[string]int64
	lists map[string][]int64
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: map[string]int64{}, lists: map[string][]int64{}}
}

// Client returns a client for the store.
func (s *MemoryStore) Client() Client { return &memClient{s: s} }

type memClient struct{ s *MemoryStore }

func (c *memClient) Close() error { return nil }

func (c *memClient) Invoke(ctx context.Context, op history.Op) history.Op {
	// A little latency makes operations overlap like they would for real.
	select {
	case <-time.After(time.Duration(rand.IntN(2000)) * time.Microsecond):
	case <-ctx.Done():
		out := complete(op, Classify(op, ctx.Err()))
		out.Error = ctx.Err().Error()
		return out
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	out := complete(op, history.OK)
	switch op.F {
	case history.FRead:
		if v, ok := c.s.data[op.Key]; ok {
			out.Value = history.Int(v)
		}
	case history.FWrite:
		c.s.data[op.Key] = *op.Value
	case history.FCas:
		if v, ok := c.s.data[op.Key]; ok && op.Old != nil && v == *op.Old {
			c.s.data[op.Key] = *op.Value
		} else {
			out.Type = history.Fail
		}
	case history.FTxn:
		// Strictly serializable: the whole transaction runs under the lock.
		if isLedgerTxn(op) {
			return c.ledgerTxn(op)
		}
		idemp := isSettlementTxn(op)
		out.Txn = make([]history.MicroOp, len(op.Txn))
		for i, m := range op.Txn {
			out.Txn[i] = m
			switch m.F {
			case history.MicroAppend:
				if !idemp || !slices.Contains(c.s.lists[m.Key], *m.Value) {
					c.s.lists[m.Key] = append(c.s.lists[m.Key], *m.Value)
				}
			case history.MicroRead:
				out.Txn[i].Values = append([]int64{}, c.s.lists[m.Key]...)
			}
		}
	default:
		out.Type = history.Fail
		out.Error = "unsupported op " + op.F
	}
	return out
}

func (c *memClient) ledgerTxn(op history.Op) history.Op {
	if readOnly(op) {
		out := complete(op, history.OK)
		out.Txn = make([]history.MicroOp, len(op.Txn))
		for i, m := range op.Txn {
			bal, ok := c.s.data[m.Key]
			if !ok {
				bal = DefaultLedgerInitialBalance
			}
			out.Txn[i] = history.MicroOp{F: history.MicroRead, Key: m.Key, Value: history.Int(bal)}
		}
		return out
	}
	fromKey, toKey, amt, ok := parseTransferAccounts(op)
	if !ok || fromKey == toKey {
		return complete(op, history.Fail)
	}
	oldFrom, ok := c.s.data[fromKey]
	if !ok {
		oldFrom = DefaultLedgerInitialBalance
	}
	oldTo, ok := c.s.data[toKey]
	if !ok {
		oldTo = DefaultLedgerInitialBalance
	}
	if oldFrom < amt {
		return complete(op, history.Fail)
	}
	newFrom := oldFrom - amt
	newTo := oldTo + amt
	c.s.data[fromKey] = newFrom
	c.s.data[toKey] = newTo
	out := complete(op, history.OK)
	out.Txn = []history.MicroOp{
		{F: history.MicroRead, Key: fromKey, Value: history.Int(oldFrom)},
		{F: history.MicroWrite, Key: fromKey, Value: history.Int(newFrom)},
		{F: history.MicroRead, Key: toKey, Value: history.Int(oldTo)},
		{F: history.MicroWrite, Key: toKey, Value: history.Int(newTo)},
	}
	return out
}
