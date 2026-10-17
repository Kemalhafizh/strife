package stats

import (
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	ms := time.Millisecond
	s := Summarize([]time.Duration{4 * ms, 1 * ms, 3 * ms, 2 * ms})
	if s.Count != 4 || s.Min != ms || s.Max != 4*ms || s.Mean != 2500*time.Microsecond || s.P50 != 2*ms || s.P95 != 4*ms {
		t.Fatalf("got %+v", s)
	}
	if Summarize(nil) != (Summary{}) {
		t.Fatal("empty input must give the zero summary")
	}
	// Nearest rank: with 100 samples 1..100ms, P95 is exactly the 95th.
	var hundred []time.Duration
	for i := 100; i >= 1; i-- {
		hundred = append(hundred, time.Duration(i)*ms)
	}
	if s := Summarize(hundred); s.P95 != 95*ms || s.P99 != 99*ms || s.P50 != 50*ms {
		t.Fatalf("nearest-rank percentiles wrong: %+v", s)
	}
}

func TestRound(t *testing.T) {
	for in, want := range map[time.Duration]string{
		412345 * time.Nanosecond:   "412µs",
		3274567 * time.Nanosecond:  "3.27ms",
		1204567 * time.Microsecond: "1.205s",
	} {
		if got := Round(in).String(); got != want {
			t.Errorf("Round(%v) = %s, want %s", in, got, want)
		}
	}
}
