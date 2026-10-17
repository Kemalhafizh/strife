package agent

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/probe"
)

func startTestServer(t *testing.T, token string) (*Server, *Client, func()) {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	cfg := ServerConfig{
		NodeName:  "test-node-1",
		Interface: "lo",
		Token:     token,
	}
	srv := NewServer(cfg)

	go func() {
		_ = srv.Serve(l)
	}()

	client := NewClient(l.Addr().String(), token)

	cleanup := func() {
		_ = srv.Shutdown(context.Background())
		_ = l.Close()
	}

	return srv, client, cleanup
}

func TestServerHealthAndPing(t *testing.T) {
	_, client, cleanup := startTestServer(t, "")
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, err := client.Ping(ctx)
	if err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	if status.Node != "test-node-1" {
		t.Errorf("expected node test-node-1, got %s", status.Node)
	}
	if status.ActiveFaults != 0 {
		t.Errorf("expected 0 active faults, got %d", status.ActiveFaults)
	}
	if status.Capabilities.OS == "" {
		t.Errorf("expected non-empty OS capability")
	}
}

func TestServerAuth(t *testing.T) {
	secretToken := "secret-token-12345"
	_, validClient, cleanup := startTestServer(t, secretToken)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Valid token should succeed
	_, err := validClient.Ping(ctx)
	if err != nil {
		t.Fatalf("valid client ping failed: %v", err)
	}

	// Invalid token should fail with 401
	badClient := NewClient(validClient.addr, "wrong-token")
	_, err = badClient.Ping(ctx)
	if err == nil {
		t.Fatalf("expected error with bad token, got nil")
	}
}

func TestServerApplyPartition(t *testing.T) {
	_, client, cleanup := startTestServer(t, "")
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Inject partition
	injectReq := ApplyRequest{
		Action:    ActionInject,
		FaultID:   1,
		Kind:      nemesis.Partition,
		TargetIPs: []string{"192.168.1.10", "192.168.1.11"},
	}
	resp, err := client.Apply(ctx, injectReq)
	if err != nil {
		t.Fatalf("apply inject failed: %v", err)
	}
	if !resp.Success {
		t.Errorf("expected success, got error %s", resp.Error)
	}
	if resp.Method != "filter:iptables" {
		t.Errorf("expected filter:iptables method, got %s", resp.Method)
	}

	// Verify active fault count
	status, err := client.Ping(ctx)
	if err != nil {
		t.Fatalf("ping failed: %v", err)
	}
	if status.ActiveFaults != 1 {
		t.Errorf("expected 1 active fault, got %d", status.ActiveFaults)
	}

	// Heal partition
	healReq := ApplyRequest{
		Action:  ActionHeal,
		FaultID: 1,
		Kind:    nemesis.Partition,
	}
	healResp, err := client.Apply(ctx, healReq)
	if err != nil {
		t.Fatalf("apply heal failed: %v", err)
	}
	if !healResp.Success {
		t.Errorf("heal failed: %s", healResp.Error)
	}

	// Verify 0 active faults
	status, _ = client.Ping(ctx)
	if status.ActiveFaults != 0 {
		t.Errorf("expected 0 active faults after heal, got %d", status.ActiveFaults)
	}
}

func TestServerApplyNetem(t *testing.T) {
	srv, client, cleanup := startTestServer(t, "")
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := ApplyRequest{
		Action:  ActionInject,
		FaultID: 2,
		Kind:    nemesis.Latency,
		Iface:   "lo",
		Delay:   50 * time.Millisecond,
		Jitter:  5 * time.Millisecond,
	}

	resp, _ := client.Apply(ctx, req)
	if resp != nil && resp.Success {
		if resp.Method != "netlink:netem" {
			t.Errorf("expected netlink:netem, got %s", resp.Method)
		}
	}

	// Teardown
	srv.Teardown()
	status, _ := client.Ping(ctx)
	if status.ActiveFaults != 0 {
		t.Errorf("expected 0 faults after teardown, got %d", status.ActiveFaults)
	}
}

func TestServerCgroup(t *testing.T) {
	tmpDir := t.TempDir()
	freezeFile := filepath.Join(tmpDir, "cgroup.freeze")
	ioFile := filepath.Join(tmpDir, "io.max")

	cgroup := NewCgroupEngine()
	if err := cgroup.Freeze(tmpDir); err != nil {
		t.Fatalf("freeze failed: %v", err)
	}
	data, _ := os.ReadFile(freezeFile)
	if string(data) != "1\n" {
		t.Errorf("expected 1\\n, got %q", string(data))
	}

	if err := cgroup.Unfreeze(tmpDir); err != nil {
		t.Fatalf("unfreeze failed: %v", err)
	}
	data, _ = os.ReadFile(freezeFile)
	if string(data) != "0\n" {
		t.Errorf("expected 0\\n, got %q", string(data))
	}

	if err := cgroup.IOStall(tmpDir, "8:0"); err != nil {
		t.Fatalf("io-stall failed: %v", err)
	}
	data, _ = os.ReadFile(ioFile)
	if string(data) != "8:0 rbps=1 wbps=1 riops=1 wiops=1\n" {
		t.Errorf("unexpected io.max content: %q", string(data))
	}

	if err := cgroup.HealIOStall(tmpDir, "8:0"); err != nil {
		t.Fatalf("heal io-stall failed: %v", err)
	}
	data, _ = os.ReadFile(ioFile)
	if string(data) != "8:0 rbps=max wbps=max riops=max wiops=max\n" {
		t.Errorf("unexpected io.max content: %q", string(data))
	}
}

func TestServerExec(t *testing.T) {
	_, client, cleanup := startTestServer(t, "")
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Simple echo execution
	out, err := client.Exec(ctx, "echo 'hello strife'")
	if err != nil {
		t.Logf("exec error (expected if sh is not on Windows without Git bash): %v", err)
	} else if out != "hello strife" {
		t.Errorf("expected 'hello strife', got %q", out)
	}
}

func TestLatencyCollector(t *testing.T) {
	c := NewLatencyCollector()
	for i, ms := range []int{2, 4, 1, 3} {
		c.Record(LatencySample{FaultID: i, Kind: nemesis.Partition, RoundTrip: time.Duration(ms) * time.Millisecond})
	}
	sum := c.Summary()
	if sum.Count != 4 || sum.Min != time.Millisecond || sum.Max != 4*time.Millisecond || sum.P50 != 2*time.Millisecond {
		t.Fatalf("got %+v", sum)
	}
	if len(c.Samples()) != 4 {
		t.Fatal("samples lost")
	}
}

func TestServerProbesEndpoint(t *testing.T) {
	srv, client, cleanup := startTestServer(t, "")
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srv.Probes().RecordFsyncStall("etcd", "/var/lib/etcd/member/wal/0.wal", 125*time.Millisecond)
	srv.Probes().RecordSignal("etcd", 4321, "SIGSTOP", "agent fault 7 pause etcd")

	events, err := client.Probes(ctx, true)
	if err != nil {
		t.Fatalf("client.Probes failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 probe events, got %d", len(events))
	}
	if events[0].Kind != probe.KindFsyncStall || events[0].Node != "test-node-1" {
		t.Errorf("unexpected first event: %+v", events[0])
	}
	if events[1].Kind != probe.KindProcessSignal || events[1].Signal != "SIGSTOP" {
		t.Errorf("unexpected second event: %+v", events[1])
	}

	// After reset=true, subsequent fetch should be empty.
	after, err := client.Probes(ctx, false)
	if err != nil {
		t.Fatalf("second client.Probes failed: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("expected 0 events after reset, got %d", len(after))
	}
}
