package sim

import (
	"testing"
	"time"
)

type recorder struct {
	w    *World
	id   NodeID
	got  []string
	boot int
}

func (r *recorder) Start() { r.boot++ }
func (r *recorder) Receive(from NodeID, msg any) {
	r.got = append(r.got, msg.(string))
}

func newPair() (*World, *recorder, *recorder) {
	w := NewWorld(Config{Seed: 1})
	a, b := &recorder{w: w, id: 0}, &recorder{w: w, id: 1}
	w.Add(0, a)
	w.Add(1, b)
	w.Start()
	return w, a, b
}

func TestPartitionDropsAndHeals(t *testing.T) {
	w, _, b := newPair()
	w.Partition([]NodeID{0}, []NodeID{1})
	w.Send(0, 1, "lost")
	w.Run(int64(time.Second))
	w.Heal([]NodeID{0}, []NodeID{1})
	w.Send(0, 1, "delivered")
	w.Run(int64(2 * time.Second))
	if len(b.got) != 1 || b.got[0] != "delivered" {
		t.Fatalf("got %v", b.got)
	}
}

func TestFreezeHoldsThenDeliversInOrder(t *testing.T) {
	w, _, b := newPair()
	w.Freeze(1)
	w.Send(0, 1, "a")
	w.Run(int64(time.Millisecond * 20))
	w.Send(0, 1, "b")
	w.Run(int64(time.Millisecond * 40))
	if len(b.got) != 0 {
		t.Fatalf("frozen node received %v", b.got)
	}
	w.Thaw(1)
	w.Run(int64(time.Second))
	if len(b.got) != 2 || b.got[0] != "a" || b.got[1] != "b" {
		t.Fatalf("got %v", b.got)
	}
}

func TestCrashLosesTimersAndUnsyncedWrites(t *testing.T) {
	w, a, _ := newPair()
	fired, synced := false, false
	w.After(0, 10*time.Millisecond, func() { fired = true })
	w.StallIO(0)
	w.Fsync(0, func() { synced = true })
	w.Run(int64(5 * time.Millisecond))
	w.Crash(0)
	w.ResumeIO(0)
	w.Restart(0)
	w.Run(int64(time.Second))
	if fired || synced {
		t.Fatalf("crash must drop timers (%v) and unsynced writes (%v)", fired, synced)
	}
	if a.boot != 2 {
		t.Fatalf("expected a reboot, boots=%d", a.boot)
	}
}

func TestIOStallBlocksFsyncUntilResume(t *testing.T) {
	w, _, _ := newPair()
	var order []int
	w.StallIO(0)
	w.Fsync(0, func() { order = append(order, 1) })
	w.Fsync(0, func() { order = append(order, 2) })
	w.Run(int64(time.Second))
	if len(order) != 0 {
		t.Fatal("fsync completed during stall")
	}
	w.ResumeIO(0)
	w.Run(int64(2 * time.Second))
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("got %v", order)
	}
}

func TestClockSkewAndJump(t *testing.T) {
	w, _, _ := newPair()
	w.SetClockRate(0, 2)
	fired := int64(-1)
	w.After(0, 100*time.Millisecond, func() { fired = w.Now() })
	w.Run(int64(time.Second))
	if fired != int64(50*time.Millisecond) {
		t.Fatalf("fast clock timer fired at %v", time.Duration(fired))
	}
	before := w.LocalNow(0)
	w.JumpClock(0, -10*time.Second)
	if got := before - w.LocalNow(0); got != int64(10*time.Second) {
		t.Fatalf("jump moved clock by %v", time.Duration(got))
	}
	if w.LocalNow(1) != w.Now() {
		t.Fatal("unskewed node drifted")
	}
}

func TestTraceHashDeterministic(t *testing.T) {
	run := func(seed uint64) string {
		w := NewWorld(Config{Seed: seed})
		a, b := &recorder{}, &recorder{}
		w.Add(0, a)
		w.Add(1, b)
		w.Start()
		for i := 0; i < 50; i++ {
			w.Send(0, 1, "x")
			w.Send(1, 0, "y")
		}
		w.SetLoss(1, 0.3)
		for i := 0; i < 50; i++ {
			w.Send(0, 1, "z")
		}
		w.Run(int64(time.Second))
		return w.TraceHash()
	}
	if run(5) != run(5) {
		t.Fatal("same seed, different trace")
	}
	if run(5) == run(6) {
		t.Fatal("different seeds, same trace")
	}
}
