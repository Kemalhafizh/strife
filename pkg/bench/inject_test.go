package bench

import (
	"context"
	"strings"
	"testing"
	"time"

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
