package bench

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/agent"
	"github.com/kemalhafizh/strife/pkg/client"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/runner"
	"github.com/kemalhafizh/strife/pkg/transport"
)

func TestInjectDryRun(t *testing.T) {
	cfg := &runner.Config{
		Nodes: []transport.Node{{Name: "n1", IP: "10.0.0.1"}, {Name: "n2", IP: "10.0.0.2"}, {Name: "n3", IP: "10.0.0.3"}},
		DB:    runner.DBConfig{Config: client.Config{Kind: "memory"}, Process: "db", Start: "/start.sh"},
	}
	res, err := Inject(context.Background(), InjectConfig{
		Cluster: cfg, Transports: []string{"dry-run"}, Kinds: []nemesis.Kind{nemesis.Partition, nemesis.Pause},
		Cycles: 4, Period: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 4 {
		t.Fatalf("want inject+heal rows for 2 kinds, got %d", len(res.Rows))
	}
	for _, r := range res.Rows {
		if r.Latency.Count != 4 || r.Errors != 0 || r.Latency.Min < 0 || r.Latency.Max > time.Second {
			t.Fatalf("implausible row %+v", r)
		}
	}
	md := res.Markdown()
	for _, want := range []string{"| dry-run | partition | inject | 4 |", "Host:", "planned instant"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func TestFaultRotatesNodes(t *testing.T) {
	if f := Fault(nemesis.Latency, 4, 3); f.Nodes[0] != 1 || f.Delay != 50*time.Millisecond {
		t.Fatalf("%+v", f)
	}
}

// timedAgent stands in for the agent transport: every command takes 2ms of
// round trip, of which the agent reports 300µs spent applying it.
type timedAgent struct {
	transport.DryRun
	col *agent.LatencyCollector
}

func (a *timedAgent) Collector() *agent.LatencyCollector { return a.col }

func (a *timedAgent) Exec(ctx context.Context, n transport.Node, script string) (string, error) {
	time.Sleep(2 * time.Millisecond)
	a.col.Record(agent.LatencySample{Node: n.Name, RoundTrip: 2 * time.Millisecond, Server: 300 * time.Microsecond})
	return a.DryRun.Exec(ctx, n, script)
}

func TestAgentSideTimeIsReported(t *testing.T) {
	cfg := &runner.Config{
		Nodes: []transport.Node{{Name: "n1", IP: "10.0.0.1"}, {Name: "n2", IP: "10.0.0.2"}},
		DB:    runner.DBConfig{Config: client.Config{Kind: "memory"}, Process: "db", Start: "/start.sh"},
	}
	tr := &timedAgent{col: agent.NewLatencyCollector()}
	inj := &runner.Injector{Transport: tr, Nodes: cfg.Nodes, Env: cfg.Env()}
	rows := measure(context.Background(), inj, "agent", nemesis.Pause, 3, 5*time.Millisecond, 2)
	for _, r := range rows {
		if r.Agent == nil || r.Agent.Count != 3 || r.Agent.P50 != 300*time.Microsecond {
			t.Fatalf("%s: agent-side %+v", r.Edge, r.Agent)
		}
		if r.Latency.P50 < 2*time.Millisecond {
			t.Fatalf("%s: latency %v below the round trip", r.Edge, r.Latency.P50)
		}
	}
	md := (&InjectResult{Rows: rows, Cycles: 3}).Markdown()
	if !strings.Contains(md, "300µs / 300µs") || !strings.Contains(md, "Agent-side:") || !strings.Contains(md, "| agent | pause |") {
		t.Fatalf("markdown:\n%s", md)
	}
}
