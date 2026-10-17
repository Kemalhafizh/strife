// Package stats summarises latency samples the same way everywhere Strife
// reports them, so a P95 in a run report and a P95 in a benchmark mean the
// same thing.
package stats

import (
	"math"
	"sort"
	"time"
)

// Summary describes a set of durations. Percentiles use the nearest-rank
// method: P95 is the smallest sample that is at least as large as 95% of all
// samples, so it is always a value that was actually observed.
type Summary struct {
	Count  int           `json:"count"`
	Min    time.Duration `json:"min"`
	Mean   time.Duration `json:"mean"`
	P50    time.Duration `json:"p50"`
	P90    time.Duration `json:"p90"`
	P95    time.Duration `json:"p95"`
	P99    time.Duration `json:"p99"`
	Max    time.Duration `json:"max"`
	StdDev time.Duration `json:"std_dev"`
}

// Summarize computes a Summary. It does not modify samples.
func Summarize(samples []time.Duration) Summary {
	if len(samples) == 0 {
		return Summary{}
	}
	s := append([]time.Duration(nil), samples...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	var sum float64
	for _, d := range s {
		sum += float64(d)
	}
	mean := sum / float64(len(s))
	var sq float64
	for _, d := range s {
		diff := float64(d) - mean
		sq += diff * diff
	}
	return Summary{
		Count:  len(s),
		Min:    s[0],
		Mean:   time.Duration(mean),
		P50:    Percentile(s, 0.50),
		P90:    Percentile(s, 0.90),
		P95:    Percentile(s, 0.95),
		P99:    Percentile(s, 0.99),
		Max:    s[len(s)-1],
		StdDev: time.Duration(math.Sqrt(sq / float64(len(s)))),
	}
}

// Percentile returns the nearest-rank p-quantile (0 < p <= 1) of sorted.
func Percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[min(max(i, 0), len(sorted)-1)]
}

// Round renders d with a precision that suits its magnitude: 412µs, 3.27ms,
// 1.204s.
func Round(d time.Duration) time.Duration {
	switch {
	case d >= time.Second:
		return d.Round(time.Millisecond)
	case d >= 10*time.Millisecond:
		return d.Round(100 * time.Microsecond)
	case d >= time.Millisecond:
		return d.Round(10 * time.Microsecond)
	default:
		return d.Round(time.Microsecond)
	}
}
