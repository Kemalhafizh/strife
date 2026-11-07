package report

import (
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

func TestComputeResilienceMTTRAndAvailability(t *testing.T) {
	sched := nemesis.Schedule{
		Nodes:    3,
		Duration: 10 * time.Second,
		Faults: []nemesis.Fault{
			{ID: 1, Kind: nemesis.Partition, Start: 2 * time.Second, End: 5 * time.Second},
		},
	}

	ops := []history.Op{
		// Healthy op before fault [1.0s -> 1.1s] OK
		{Index: 0, Process: 0, Type: history.Invoke, F: history.FWrite, Key: "k0", Value: history.Int(1), Time: int64(1000 * time.Millisecond)},
		{Index: 1, Process: 0, Type: history.OK, F: history.FWrite, Key: "k0", Value: history.Int(1), Time: int64(1100 * time.Millisecond)},
		// Op during fault [3.0s -> 3.5s] Fail
		{Index: 2, Process: 1, Type: history.Invoke, F: history.FRead, Key: "k0", Time: int64(3000 * time.Millisecond)},
		{Index: 3, Process: 1, Type: history.Fail, F: history.FRead, Key: "k0", Time: int64(3500 * time.Millisecond)},
		// First op completing after fault heal at 5.0s: completes at 5.25s -> MTTR = 250ms
		{Index: 4, Process: 0, Type: history.Invoke, F: history.FRead, Key: "k0", Time: int64(5100 * time.Millisecond)},
		{Index: 5, Process: 0, Type: history.OK, F: history.FRead, Key: "k0", Value: history.Int(1), Time: int64(5250 * time.Millisecond)},
	}

	res := ComputeResilience(sched, nil, ops)
	if res == nil {
		t.Fatalf("expected non-nil ResilienceMetrics")
	}
	if res.HealthyWindowAvailability != 1.0 {
		t.Fatalf("expected 1.0 healthy window availability, got %v", res.HealthyWindowAvailability)
	}
	if res.FaultWindowAvailability != 0.0 {
		t.Fatalf("expected 0.0 fault window availability, got %v", res.FaultWindowAvailability)
	}
	if res.MTTR.Count != 1 || res.MTTR.P50 != 250*time.Millisecond {
		t.Fatalf("expected MTTR p50=250ms, got %+v", res.MTTR)
	}
}
