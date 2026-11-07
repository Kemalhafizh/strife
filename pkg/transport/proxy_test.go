package transport

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/nemesis"
)

func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String()
}

func TestProxyFaultInjectionAndHeal(t *testing.T) {
	ctx := context.Background()
	upstream := startEchoServer(t)

	p := NewProxy()
	nodes := []Node{{Name: "n1", IP: "127.0.0.1"}}
	env := nemesis.Env{Nodes: []nemesis.Target{{Name: "n1", IP: "127.0.0.1"}}}

	wrapped, closeFn, err := p.WrapEndpoints(ctx, nodes, []string{"http://" + upstream})
	if err != nil {
		t.Fatalf("WrapEndpoints: %v", err)
	}
	defer closeFn()
	if len(wrapped) != 1 || wrapped[0] == "http://"+upstream {
		t.Fatalf("expected rewritten endpoint, got %v", wrapped)
	}
	proxyAddr := endpointHostPort(wrapped[0])

	echoRoundTrip := func(timeout time.Duration) (time.Duration, error) {
		start := time.Now()
		c, err := net.DialTimeout("tcp", proxyAddr, timeout)
		if err != nil {
			return 0, err
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(timeout))
		if _, err := c.Write([]byte("ping")); err != nil {
			return 0, err
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil {
			return 0, err
		}
		return time.Since(start), nil
	}

	if _, err := echoRoundTrip(time.Second); err != nil {
		t.Fatalf("baseline round-trip failed: %v", err)
	}

	// 1. Latency fault adds measurable delay.
	latFault := nemesis.Fault{ID: 1, Kind: nemesis.Latency, Nodes: []int{0}, Delay: 40 * time.Millisecond}
	if _, err := p.InjectFault(ctx, nodes[0], latFault, env); err != nil {
		t.Fatalf("InjectFault latency: %v", err)
	}
	dur, err := echoRoundTrip(time.Second)
	if err != nil {
		t.Fatalf("round-trip under latency failed: %v", err)
	}
	if dur < 35*time.Millisecond {
		t.Fatalf("expected >=35ms round-trip under 40ms latency fault, got %v", dur)
	}
	if _, err := p.HealFault(ctx, nodes[0], latFault, env); err != nil {
		t.Fatalf("HealFault latency: %v", err)
	}

	// 2. Kill / Partition severs connections and rejects new traffic until healed.
	killFault := nemesis.Fault{ID: 2, Kind: nemesis.Kill, Nodes: []int{0}}
	if _, err := p.InjectFault(ctx, nodes[0], killFault, env); err != nil {
		t.Fatalf("InjectFault kill: %v", err)
	}
	if _, err := echoRoundTrip(200 * time.Millisecond); err == nil {
		t.Fatalf("expected connection failure while node is killed")
	}
	if _, err := p.HealFault(ctx, nodes[0], killFault, env); err != nil {
		t.Fatalf("HealFault kill: %v", err)
	}
	if _, err := echoRoundTrip(time.Second); err != nil {
		t.Fatalf("round-trip after healing kill failed: %v", err)
	}

	// 3. Pause / Freeze stalls in-flight traffic until healed.
	pauseFault := nemesis.Fault{ID: 3, Kind: nemesis.Pause, Nodes: []int{0}}
	if _, err := p.InjectFault(ctx, nodes[0], pauseFault, env); err != nil {
		t.Fatalf("InjectFault pause: %v", err)
	}
	go func() {
		time.Sleep(60 * time.Millisecond)
		_, _ = p.HealFault(ctx, nodes[0], pauseFault, env)
	}()
	dur, err = echoRoundTrip(time.Second)
	if err != nil {
		t.Fatalf("round-trip across pause/heal failed: %v", err)
	}
	if dur < 45*time.Millisecond {
		t.Fatalf("expected stalled connection to wait for heal (>=45ms), got %v", dur)
	}
}

func TestDoctorInspect(t *testing.T) {
	ctx := context.Background()
	upstream := startEchoServer(t)

	rep := Inspect(ctx, []string{upstream, "127.0.0.1:1"})
	if rep.OS == "" || rep.Arch == "" {
		t.Fatalf("expected OS and Arch in DoctorReport: %+v", rep)
	}
	if len(rep.Endpoints) != 2 {
		t.Fatalf("expected 2 endpoint checks, got %d", len(rep.Endpoints))
	}
	if !rep.Endpoints[0].Reachable {
		t.Fatalf("expected upstream %s to be reachable: %+v", upstream, rep.Endpoints[0])
	}
	if rep.Endpoints[1].Reachable {
		t.Fatalf("expected 127.0.0.1:1 to be unreachable")
	}
}
