package agent

import (
	"sync"
	"time"

	"github.com/kemalhafizh/strife/internal/stats"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// LatencySample times one fault transition sent to an agent.
type LatencySample struct {
	FaultID int          `json:"fault_id"`
	Kind    nemesis.Kind `json:"kind"`
	Node    string       `json:"node"`
	Start   bool         `json:"start"` // inject (true) or heal (false)
	// RoundTrip is what the caller waited: from sending the request until
	// the agent confirmed the kernel call returned.
	RoundTrip time.Duration `json:"round_trip"`
	// Server is the part of RoundTrip spent inside the agent applying the
	// fault (request decoded → kernel call returned).
	Server time.Duration `json:"server"`
	Method string        `json:"method"`
}

// LatencyCollector accumulates samples; it is safe for concurrent use.
type LatencyCollector struct {
	mu      sync.Mutex
	samples []LatencySample
}

// NewLatencyCollector returns an empty collector.
func NewLatencyCollector() *LatencyCollector { return &LatencyCollector{} }

// Record adds a sample.
func (c *LatencyCollector) Record(s LatencySample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, s)
}

// Samples returns a copy of every sample.
func (c *LatencyCollector) Samples() []LatencySample {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]LatencySample(nil), c.samples...)
}

// Summary summarises the round-trip latencies.
func (c *LatencyCollector) Summary() stats.Summary {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := make([]time.Duration, len(c.samples))
	for i, s := range c.samples {
		d[i] = s.RoundTrip
	}
	return stats.Summarize(d)
}
