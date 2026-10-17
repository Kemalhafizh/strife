package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/client"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/transport"
)

func testConfig() *Config {
	c := &Config{
		Name:     "memory",
		Seed:     7,
		Duration: Duration(1500 * time.Millisecond),
		Nodes: []transport.Node{
			{Name: "n1", IP: "10.0.0.1", Cgroup: "/sys/fs/cgroup/n1"},
			{Name: "n2", IP: "10.0.0.2", Cgroup: "/sys/fs/cgroup/n2"},
			{Name: "n3", IP: "10.0.0.3", Cgroup: "/sys/fs/cgroup/n3"},
		},
		Transport: transport.Config{Type: "dry-run"},
		DB:        DBConfig{Config: client.Config{Kind: "memory"}, Process: "db", Start: "/start.sh"},
		Workload:  WorkloadConfig{Think: Duration(5 * time.Millisecond)},
		Nemesis: NemesisConfig{
			Faults:   []nemesis.Kind{nemesis.Partition, nemesis.Kill, nemesis.Latency, nemesis.IOStall},
			Interval: Duration(200 * time.Millisecond),
			Length:   Duration(200 * time.Millisecond),
			IODevice: "8:0",
		},
	}
	c.Defaults()
	return c
}

func TestLiveRunAgainstMemoryStore(t *testing.T) {
	cfg := testConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	dry := &transport.DryRun{}
	res, err := Run(context.Background(), cfg, Options{Transport: dry})
	if err != nil {
		t.Fatal(err)
	}
	if res.Check.Verdict != checker.Linearizable {
		t.Fatalf("an atomic in-memory store must be linearizable, got %s", res.Check.Verdict)
	}
	if res.Check.Stats.OK < 50 {
		t.Fatalf("workload too small: %+v", res.Check.Stats)
	}
	if len(res.Schedule.Faults) == 0 || len(res.Applied) != 2*len(res.Schedule.Faults) {
		t.Fatalf("faults %d, applied edges %d", len(res.Schedule.Faults), len(res.Applied))
	}
	log := strings.Join(dry.Log, "\n")
	for _, want := range []string{"iptables -N STRIFE", "iptables -F STRIFE"} {
		if !strings.Contains(log, want) {
			t.Fatalf("command log missing %q:\n%s", want, log)
		}
	}
}

func TestScheduleIsSeeded(t *testing.T) {
	a, b := testConfig(), testConfig()
	if a.Schedule().Hash() != b.Schedule().Hash() {
		t.Fatal("same seed, different schedule")
	}
	b.Seed++
	if a.Schedule().Hash() == b.Schedule().Hash() {
		t.Fatal("different seed, same schedule")
	}
}

func TestLoadRejectsBadConfigs(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "c.json")
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := map[string]string{
		"unknown field": `{"nodes":[{"name":"a","ip":"10.0.0.1"}],"bogus":1}`,
		"no nodes":      `{"nodes":[]}`,
		"bad ip":        `{"nodes":[{"name":"a","ip":"evil;rm"}]}`,
		"skew in docker": `{"nodes":[{"name":"a","ip":"10.0.0.1"}],"transport":{"type":"docker"},
			"nemesis":{"faults":["clock-skew"]}}`,
		"kill without start": `{"nodes":[{"name":"a","ip":"10.0.0.1"}],"db":{"kind":"memory","process":"x"},
			"nemesis":{"faults":["kill"]}}`,
	}
	for name, body := range cases {
		if _, err := Load(write(body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	ok := `{"nodes":[{"name":"a","ip":"10.0.0.1"}],"duration":"5s","db":{"kind":"memory"},"nemesis":{"faults":["partition"]}}`
	c, err := Load(write(ok))
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(c.Duration) != 5*time.Second || c.Workload.Concurrency != 2 {
		t.Fatalf("defaults not applied: %+v", c)
	}
}

func TestLiveAppendWorkload(t *testing.T) {
	cfg := testConfig()
	cfg.Workload = WorkloadConfig{Model: "append", Keys: 3, Concurrency: 6, Think: Duration(2 * time.Millisecond), WritesPerKey: 12, Isolation: "strict-serializable"}
	cfg.Defaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), cfg, Options{Transport: &transport.DryRun{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Check.Model != "append" || res.Check.Verdict != checker.Linearizable {
		t.Fatalf("a strictly serializable store must pass: %s %s", res.Check.Model, res.Check.Verdict)
	}
	// Keys rotated: more distinct keys than slots, and every value unique.
	keys, values := map[string]bool{}, map[int64]bool{}
	reads := 0
	for _, op := range res.History {
		for _, m := range op.Txn {
			keys[m.Key] = true
			if op.Type == "invoke" && m.F == "append" {
				if values[*m.Value] {
					t.Fatalf("value %d appended twice", *m.Value)
				}
				values[*m.Value] = true
			}
			if op.Type == "ok" && m.F == "r" && len(m.Values) > 0 {
				reads++
			}
		}
	}
	if len(keys) <= 3 || reads == 0 {
		t.Fatalf("keys %d (want rotation past 3 slots), non-empty reads %d", len(keys), reads)
	}
}

func TestAppendCatchesALostAppend(t *testing.T) {
	cfg := testConfig()
	cfg.Workload = WorkloadConfig{Model: "append", Keys: 2, Concurrency: 4, Think: Duration(2 * time.Millisecond), Isolation: "strict-serializable"}
	cfg.Defaults()
	store := client.NewMemoryStore()
	var n atomic.Int32
	lossy := func(i int) (client.Client, error) { return &dropFirstAppend{Client: store.Client(), n: &n}, nil }
	res, err := Run(context.Background(), cfg, Options{Transport: &transport.DryRun{}, Clients: lossy})
	if err != nil {
		t.Fatal(err)
	}
	if res.Check.Verdict != checker.Violation {
		t.Fatalf("an acknowledged but lost append must be caught, got %s", res.Check.Verdict)
	}
}

// dropFirstAppend acknowledges the first append transaction without
// applying it.
type dropFirstAppend struct {
	client.Client
	n *atomic.Int32
}

func (d *dropFirstAppend) Invoke(ctx context.Context, op history.Op) history.Op {
	if op.F == history.FTxn && len(op.Txn) == 1 && op.Txn[0].F == history.MicroAppend && d.n.CompareAndSwap(0, 1) {
		return history.Op{Process: op.Process, Type: history.OK, F: op.F, Txn: op.Txn}
	}
	return d.Client.Invoke(ctx, op)
}

func TestRedactedConfig(t *testing.T) {
	cfg := testConfig()
	cfg.DB.Password, cfg.Transport.Token = "hunter2", "env:STRIFE_AGENT_TOKEN"
	r := cfg.Redacted()
	if r.DB.Password != "[redacted]" || r.Transport.Token != "env:STRIFE_AGENT_TOKEN" || cfg.DB.Password != "hunter2" {
		t.Fatalf("redaction wrong: %q %q (original %q)", r.DB.Password, r.Transport.Token, cfg.DB.Password)
	}
}

func TestScheduleOverrideIsReplayed(t *testing.T) {
	cfg := testConfig()
	sched := cfg.Schedule()
	if len(sched.Faults) < 2 {
		t.Fatalf("seeded schedule too small: %d faults", len(sched.Faults))
	}
	sched.Faults = sched.Faults[1:2]
	res, err := Run(context.Background(), cfg, Options{Transport: &transport.DryRun{}, Schedule: &sched})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Schedule.Faults) != 1 || res.Schedule.Faults[0].ID != sched.Faults[0].ID || len(res.Applied) != 2 {
		t.Fatalf("schedule %+v, applied %d", res.Schedule.Faults, len(res.Applied))
	}
	bad := nemesis.Schedule{Nodes: 5}
	if _, err := Run(context.Background(), cfg, Options{Transport: &transport.DryRun{}, Schedule: &bad}); err == nil {
		t.Fatal("a schedule for another cluster size was accepted")
	}
}
