package agent

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// JitterSample records timing details for a single fault transition.
type JitterSample struct {
	FaultID      int           `json:"fault_id"`
	Kind         nemesis.Kind  `json:"kind"`
	Node         string        `json:"node"`
	Start        bool          `json:"start"`
	PlannedAt    time.Duration `json:"planned_at"`
	DispatchedAt time.Duration `json:"dispatched_at"`
	AppliedAt    time.Duration `json:"applied_at"`
	Duration     time.Duration `json:"duration"`
	Jitter       time.Duration `json:"jitter"` // AppliedAt - PlannedAt
	Method       string        `json:"method"`
}

// JitterSummary aggregates statistical percentiles of measured injection jitter.
type JitterSummary struct {
	Count  int           `json:"count"`
	Min    time.Duration `json:"min"`
	Max    time.Duration `json:"max"`
	Mean   time.Duration `json:"mean"`
	P50    time.Duration `json:"p50"`
	P90    time.Duration `json:"p90"`
	P95    time.Duration `json:"p95"`
	P99    time.Duration `json:"p99"`
	StdDev time.Duration `json:"std_dev"`
}

// JitterCollector collects timing samples across fault injections.
type JitterCollector struct {
	mu      sync.Mutex
	samples []JitterSample
}

// NewJitterCollector initializes an empty jitter collector.
func NewJitterCollector() *JitterCollector {
	return &JitterCollector{}
}

// Record adds a timing sample.
func (c *JitterCollector) Record(s JitterSample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.Jitter == 0 && s.AppliedAt >= s.PlannedAt {
		s.Jitter = s.AppliedAt - s.PlannedAt
	}
	c.samples = append(c.samples, s)
}

// Samples returns a copy of all recorded samples.
func (c *JitterCollector) Samples() []JitterSample {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]JitterSample, len(c.samples))
	copy(out, c.samples)
	return out
}

// Summary calculates statistical metrics across all recorded samples.
func (c *JitterCollector) Summary() JitterSummary {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.samples) == 0 {
		return JitterSummary{}
	}

	jitters := make([]float64, len(c.samples))
	var sum float64
	minJ := float64(c.samples[0].Jitter)
	maxJ := float64(c.samples[0].Jitter)

	for i, s := range c.samples {
		val := float64(s.Jitter)
		if val < 0 {
			val = -val
		}
		jitters[i] = val
		sum += val
		if val < minJ {
			minJ = val
		}
		if val > maxJ {
			maxJ = val
		}
	}

	sort.Float64s(jitters)
	n := float64(len(jitters))
	mean := sum / n

	var varianceSum float64
	for _, val := range jitters {
		diff := val - mean
		varianceSum += diff * diff
	}
	stdDev := math.Sqrt(varianceSum / n)

	percentile := func(p float64) time.Duration {
		if len(jitters) == 0 {
			return 0
		}
		idx := int(math.Ceil(p*float64(len(jitters)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(jitters) {
			idx = len(jitters) - 1
		}
		return time.Duration(jitters[idx])
	}

	return JitterSummary{
		Count:  len(c.samples),
		Min:    time.Duration(minJ),
		Max:    time.Duration(maxJ),
		Mean:   time.Duration(mean),
		P50:    percentile(0.50),
		P90:    percentile(0.90),
		P95:    percentile(0.95),
		P99:    percentile(0.99),
		StdDev: time.Duration(stdDev),
	}
}

// FormatTable returns a Markdown comparison table between shell execution and strife-agent.
func (s JitterSummary) FormatTable() string {
	var b strings.Builder
	b.WriteString("| Metric | strife-agent (socket/netlink) | Typical docker exec | Improvement |\n")
	b.WriteString("|---|---|---|---|\n")
	fmt.Fprintf(&b, "| Samples | %d | - | - |\n", s.Count)
	fmt.Fprintf(&b, "| Median (p50) | %v | ~45ms | ~%.1fx faster |\n", s.P50, 45.0/math.Max(0.1, s.P50.Seconds()*1000))
	fmt.Fprintf(&b, "| 95th percentile | %v | ~80ms | ~%.1fx faster |\n", s.P95, 80.0/math.Max(0.1, s.P95.Seconds()*1000))
	fmt.Fprintf(&b, "| 99th percentile | %v | ~120ms | ~%.1fx faster |\n", s.P99, 120.0/math.Max(0.1, s.P99.Seconds()*1000))
	fmt.Fprintf(&b, "| Min / Max | %v / %v | 25ms / 250ms | - |\n", s.Min, s.Max)
	return b.String()
}
