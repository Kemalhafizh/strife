package transport

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/agent"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

func TestAgentTransport(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen error: %v", err)
	}
	defer l.Close()

	srv := agent.NewServer(agent.ServerConfig{
		NodeName:  "node-0",
		Interface: "lo",
	})
	go func() {
		_ = srv.Serve(l)
	}()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	host, portStr, _ := net.SplitHostPort(l.Addr().String())
	_ = host

	tr := NewAgent(Config{
		Type: "agent",
	})

	if tr.Name() != "agent" {
		t.Errorf("expected name 'agent', got %q", tr.Name())
	}

	node := Node{
		Name:      "node-0",
		IP:        "127.0.0.1",
		AgentAddr: l.Addr().String(),
	}

	env := nemesis.Env{
		Nodes: []nemesis.Target{
			{Name: "node-0", IP: "127.0.0.1"},
			{Name: "node-1", IP: "127.0.0.2"},
		},
		Interface: "lo",
	}

	fault := nemesis.Fault{
		ID:    1,
		Kind:  nemesis.Partition,
		Nodes: []int{0},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Inject fault
	applied, err := tr.InjectFault(ctx, node, fault, env)
	if err != nil {
		t.Fatalf("inject fault failed: %v", err)
	}
	if applied == "" {
		t.Errorf("expected non-empty applied description")
	}

	// Heal fault
	applied, err = tr.HealFault(ctx, node, fault, env)
	if err != nil {
		t.Fatalf("heal fault failed: %v", err)
	}
	if applied == "" {
		t.Errorf("expected non-empty applied description on heal")
	}

	// Teardown
	err = tr.TeardownNode(ctx, node)
	if err != nil {
		t.Fatalf("teardown node failed: %v", err)
	}

	// Check jitter summary
	summary := tr.JitterSummary()
	if summary.Count != 2 {
		t.Errorf("expected 2 samples recorded, got %d", summary.Count)
	}
	_ = portStr
}
