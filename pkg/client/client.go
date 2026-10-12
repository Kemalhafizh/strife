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
	"math/rand/v2"
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
	Kind      string   `json:"kind"`                // etcd | memory
	Endpoints []string `json:"endpoints,omitempty"` // one per node, e.g. http://10.0.0.1:2379
	Prefix    string   `json:"prefix,omitempty"`    // key prefix, default "strife/"
}

// NewFactory builds a factory for the configured driver. nodes is the number
// of nodes; memory clients all share one store.
func NewFactory(c Config, nodes int) (Factory, error) {
	switch c.Kind {
	case "etcd":
		if len(c.Endpoints) == 0 {
			return nil, fmt.Errorf("client: etcd needs db.endpoints")
		}
		prefix := c.Prefix
		if prefix == "" {
			prefix = "strife/"
		}
		return func(i int) (Client, error) {
			return NewEtcd(c.Endpoints[i%len(c.Endpoints)], prefix), nil
		}, nil
	case "memory":
		store := NewMemoryStore()
		return func(i int) (Client, error) { return store.Client(), nil }, nil
	}
	return nil, fmt.Errorf("client: unknown db kind %q (etcd, memory)", c.Kind)
}

// Classify maps a transport error to a completion type. Errors that prove
// the request never reached the server are definite failures; anything else
// on a mutating op is indeterminate. Reads never change state, so a failed
// read is always safe to call "fail".
func Classify(op history.Op, err error) history.Type {
	if err == nil {
		return history.OK
	}
	if op.F == history.FRead {
		return history.Fail
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return history.Fail
	}
	return history.Info
}

func complete(op history.Op, t history.Type) history.Op {
	out := history.Op{Process: op.Process, Type: t, F: op.F, Key: op.Key, Value: op.Value, Old: op.Old}
	if op.F == history.FRead {
		out.Value = nil
	}
	return out
}

// ---------------------------------------------------------------------------

// MemoryStore is a linearizable in-process store, the reference
// implementation used for self-tests and dry runs.
type MemoryStore struct {
	mu   sync.Mutex
	data map[string]int64
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{data: map[string]int64{}} }

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
	default:
		out.Type = history.Fail
		out.Error = "unsupported op " + op.F
	}
	return out
}
