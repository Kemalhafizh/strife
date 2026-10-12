package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/client"
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
