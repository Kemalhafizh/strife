package nemesis

import (
	"strings"
	"testing"
	"time"
)

func opts() Options {
	return Options{Nodes: 5, Duration: 60 * time.Second, Kinds: AllKinds, Interval: 3 * time.Second, Length: 4 * time.Second}
}

func TestGenerateDeterministic(t *testing.T) {
	a, b := Generate(99, opts()), Generate(99, opts())
	if a.Hash() != b.Hash() {
		t.Fatal("same seed produced different schedules")
	}
	if Generate(100, opts()).Hash() == a.Hash() {
		t.Fatal("different seeds produced identical schedules")
	}
	if len(a.Faults) < 5 {
		t.Fatalf("too few faults: %d", len(a.Faults))
	}
}

func TestGenerateInvariants(t *testing.T) {
	for seed := uint64(0); seed < 200; seed++ {
		s := Generate(seed, opts())
		laneEnd := map[string]time.Duration{}
		for _, f := range s.Faults {
			if f.End <= f.Start || f.End > s.Duration*85/100 {
				t.Fatalf("seed %d: bad window %+v", seed, f)
			}
			if f.Start < laneEnd[lane(f.Kind)] {
				t.Fatalf("seed %d: overlapping %s faults", seed, lane(f.Kind))
			}
			laneEnd[lane(f.Kind)] = f.End
			if len(f.Nodes) == 0 || len(f.Nodes) > s.Nodes {
				t.Fatalf("seed %d: bad targets %v", seed, f.Nodes)
			}
			if f.Kind == Partition && len(f.Other(s.Nodes)) == 0 {
				t.Fatalf("seed %d: partition with empty side", seed)
			}
		}
	}
}

func TestParseKinds(t *testing.T) {
	k, err := ParseKinds("partition, kill,partition")
	if err != nil || len(k) != 2 {
		t.Fatalf("got %v %v", k, err)
	}
	if _, err := ParseKinds("meteor"); err == nil {
		t.Fatal("expected error")
	}
	if k, _ := ParseKinds("all"); len(k) != len(AllKinds) {
		t.Fatal("all")
	}
}

func env() Env {
	return Env{
		Nodes: []Target{
			{Name: "n1", IP: "10.0.0.1", Cgroup: "/sys/fs/cgroup/a"},
			{Name: "n2", IP: "10.0.0.2", Cgroup: "/sys/fs/cgroup/b"},
			{Name: "n3", IP: "10.0.0.3", Cgroup: "/sys/fs/cgroup/c"},
		},
		Process: "etcd --name", Start: "/start.sh", IODevice: "8:0",
	}
}

func TestPartitionCommands(t *testing.T) {
	cmds := env().Inject(Fault{Kind: Partition, Nodes: []int{0}})
	if len(cmds) != 3 {
		t.Fatalf("want 3 commands, got %d: %+v", len(cmds), cmds)
	}
	if !strings.Contains(cmds[0].Script, "-s 10.0.0.2 -j DROP") || !strings.Contains(cmds[0].Script, "-s 10.0.0.3 -j DROP") {
		t.Fatalf("isolated side should drop both peers: %s", cmds[0].Script)
	}
	if cmds[1].Node != 1 || !strings.Contains(cmds[1].Script, "-s 10.0.0.1 -j DROP") {
		t.Fatalf("majority side should drop the isolated node: %+v", cmds[1])
	}
}

func TestProcessPatternIsQuoted(t *testing.T) {
	e := env()
	e.Process = "it's; rm -rf /"
	cmd := e.Inject(Fault{Kind: Kill, Nodes: []int{1}})[0].Script
	if cmd != `pkill -9 -f '[i]t'\''s; rm -rf /' || true` {
		t.Fatalf("unsafe quoting: %s", cmd)
	}
}

func TestValidate(t *testing.T) {
	if err := env().Validate(AllKinds); err != nil {
		t.Fatal(err)
	}
	e := env()
	e.Nodes[1].IP = "10.0.0.2; reboot"
	if err := e.Validate(nil); err == nil {
		t.Fatal("expected invalid ip error")
	}
	e = env()
	e.IODevice = ""
	if err := e.Validate([]Kind{IOStall}); err == nil {
		t.Fatal("expected io device error")
	}
}

func TestTimelineOrder(t *testing.T) {
	s := Schedule{Faults: []Fault{
		{ID: 0, Kind: Kill, Start: 1, End: 5},
		{ID: 1, Kind: Partition, Start: 5, End: 9},
	}}
	tl := s.Timeline()
	if tl[1].Start || tl[1].Fault.ID != 0 || !tl[2].Start {
		t.Fatalf("heal must precede start at equal times: %+v", tl)
	}
}

func TestTeardownRestartsKilledProcessOnce(t *testing.T) {
	cmds := env().Teardown([]Kind{Kill, Freeze})
	var inNode, onHost int
	for _, c := range cmds {
		if c.Host {
			onHost++
			continue
		}
		inNode++
		if !strings.Contains(c.Script, "pgrep -f '[e]tcd --name' >/dev/null || (/start.sh)") {
			t.Fatalf("teardown must restart a dead db exactly once: %s", c.Script)
		}
	}
	if inNode != 3 || onHost != 3 {
		t.Fatalf("got %d node and %d host commands", inNode, onHost)
	}
}
