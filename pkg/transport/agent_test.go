package transport

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
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

	// The fault is a pause of a process this test owns: it needs no
	// privilege and touches nothing on the machine (a partition would
	// program the host's firewall).
	marker := fmt.Sprintf("%d.%d", 30+os.Getpid()%1000, time.Now().Nanosecond()%1000)
	victim := exec.Command("sleep", marker)
	if err := victim.Start(); err != nil {
		t.Skipf("cannot start a process to pause: %v", err)
	}
	defer func() { _ = victim.Process.Kill(); _ = victim.Wait() }()

	env := nemesis.Env{
		Nodes: []nemesis.Target{
			{Name: "node-0", IP: "127.0.0.1"},
			{Name: "node-1", IP: "127.0.0.2"},
		},
		Interface: "lo",
		Process:   "sleep " + marker,
	}

	fault := nemesis.Fault{
		ID:    1,
		Kind:  nemesis.Pause,
		Nodes: []int{0},
	}

	state := func() string {
		if runtime.GOOS != "linux" {
			return ""
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", victim.Process.Pid))
		if err != nil {
			return ""
		}
		f := strings.Fields(string(data[strings.LastIndexByte(string(data), ')')+1:]))
		return f[0]
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
	if st := state(); st != "" && st != "T" {
		t.Errorf("process state %q after pause, want T (stopped)", st)
	}

	// Heal fault
	applied, err = tr.HealFault(ctx, node, fault, env)
	if err != nil {
		t.Fatalf("heal fault failed: %v", err)
	}
	if applied == "" {
		t.Errorf("expected non-empty applied description on heal")
	}
	if st := state(); st == "T" {
		t.Errorf("process still stopped after heal")
	}

	// Teardown
	err = tr.TeardownNode(ctx, node)
	if err != nil {
		t.Fatalf("teardown node failed: %v", err)
	}

	// Both transitions were timed, and the timings are durations, not
	// timestamps.
	summary := tr.LatencySummary()
	if summary.Count != 2 || summary.Max <= 0 || summary.Max > 10*time.Second {
		t.Errorf("expected 2 plausible samples, got %+v", summary)
	}
	for _, s := range tr.Collector().Samples() {
		if s.Server < 0 || s.Server > s.RoundTrip {
			t.Errorf("server time %v must lie within the round trip %v", s.Server, s.RoundTrip)
		}
	}
	_ = portStr
}
