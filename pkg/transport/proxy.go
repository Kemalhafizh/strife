package transport

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/internal/stats"
	"github.com/kemalhafizh/strife/pkg/agent"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// EndpointWrapper is implemented by transports that interpose local TCP
// listeners in front of database endpoints so faults can be injected in user
// space without root privileges or OS-specific kernel modules.
type EndpointWrapper interface {
	WrapEndpoints(ctx context.Context, nodes []Node, endpoints []string) ([]string, func(), error)
}

// Proxy is a user-space L4 TCP fault-injection transport that works across
// Linux, macOS, and Windows without root privileges, iptables, tc netem, or
// cgroup v2 access.
type Proxy struct {
	mu        sync.RWMutex
	byKey     map[string]*proxyNode
	listeners []net.Listener
	collector *agent.LatencyCollector
}

type proxyNode struct {
	mu      sync.Mutex
	cond    *sync.Cond
	faults  map[int]nemesis.Fault
	conns   map[net.Conn]struct{}
	dropped bool
	stalled bool
	delay   time.Duration
	jitter  time.Duration
	loss    float64
}

func newProxyNode() *proxyNode {
	pn := &proxyNode{
		faults: make(map[int]nemesis.Fault),
		conns:  make(map[net.Conn]struct{}),
	}
	pn.cond = sync.NewCond(&pn.mu)
	return pn
}

func (pn *proxyNode) recomputeLocked() (newlyDropped bool) {
	wasDropped := pn.dropped
	pn.dropped = false
	pn.stalled = false
	pn.delay = 0
	pn.jitter = 0
	pn.loss = 0

	for _, f := range pn.faults {
		switch f.Kind {
		case nemesis.Partition, nemesis.Kill:
			pn.dropped = true
		case nemesis.Pause, nemesis.Freeze, nemesis.IOStall:
			pn.stalled = true
		case nemesis.Latency:
			if f.Delay > pn.delay {
				pn.delay = f.Delay
				pn.jitter = f.Jitter
			}
		case nemesis.Loss:
			if f.Loss > pn.loss {
				pn.loss = f.Loss
			}
		}
	}
	pn.cond.Broadcast()
	return !wasDropped && pn.dropped
}

func (pn *proxyNode) applyFault(f nemesis.Fault) {
	pn.mu.Lock()
	pn.faults[f.ID] = f
	killConns := pn.recomputeLocked()
	var toClose []net.Conn
	if killConns {
		for c := range pn.conns {
			toClose = append(toClose, c)
		}
	}
	pn.mu.Unlock()
	for _, c := range toClose {
		_ = c.Close()
	}
}

func (pn *proxyNode) healFault(faultID int) {
	pn.mu.Lock()
	delete(pn.faults, faultID)
	pn.recomputeLocked()
	pn.mu.Unlock()
}

func (pn *proxyNode) reset() {
	pn.mu.Lock()
	clear(pn.faults)
	pn.recomputeLocked()
	pn.mu.Unlock()
}

func (pn *proxyNode) registerConn(c net.Conn) bool {
	pn.mu.Lock()
	defer pn.mu.Unlock()
	if pn.dropped {
		return false
	}
	pn.conns[c] = struct{}{}
	return true
}

func (pn *proxyNode) unregisterConn(c net.Conn) {
	pn.mu.Lock()
	delete(pn.conns, c)
	pn.mu.Unlock()
}

func (pn *proxyNode) waitReady() (dropped bool, delay time.Duration, loss float64) {
	pn.mu.Lock()
	for pn.stalled && !pn.dropped {
		pn.cond.Wait()
	}
	dropped = pn.dropped
	d, j := pn.delay, pn.jitter
	loss = pn.loss
	pn.mu.Unlock()

	if d > 0 {
		delay = d
		if j > 0 {
			delta := time.Duration(rand.Int64N(int64(2*j+1))) - j
			if delay+delta > 0 {
				delay += delta
			}
		}
	}
	return dropped, delay, loss
}

// NewProxy creates a user-space L4 TCP fault proxy transport.
func NewProxy() *Proxy {
	return &Proxy{
		byKey:     make(map[string]*proxyNode),
		collector: agent.NewLatencyCollector(),
	}
}

// Name implements Transport.
func (p *Proxy) Name() string { return "proxy" }

// Exec implements Transport (no-op for user-space L4 proxy).
func (p *Proxy) Exec(_ context.Context, _ Node, _ string) (string, error) {
	return "", nil
}

// HostExec implements Transport (no-op for user-space L4 proxy).
func (p *Proxy) HostExec(_ context.Context, _ Node, _ string) (string, error) {
	return "", nil
}

func (p *Proxy) nodeFor(keys ...string) *proxyNode {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range keys {
		if k == "" {
			continue
		}
		if pn, ok := p.byKey[k]; ok {
			for _, alias := range keys {
				if alias != "" {
					p.byKey[alias] = pn
				}
			}
			return pn
		}
	}
	pn := newProxyNode()
	for _, k := range keys {
		if k != "" {
			p.byKey[k] = pn
		}
	}
	return pn
}

// WrapEndpoints starts one local TCP proxy listener per endpoint and returns
// the rewritten endpoint strings along with a cleanup function.
func (p *Proxy) WrapEndpoints(ctx context.Context, nodes []Node, endpoints []string) ([]string, func(), error) {
	if len(endpoints) == 0 {
		return nil, func() {}, nil
	}
	wrapped := make([]string, len(endpoints))
	var listeners []net.Listener

	for i, ep := range endpoints {
		scheme, hostPort, pathSuffix, err := splitEndpoint(ep)
		if err != nil {
			for _, l := range listeners {
				_ = l.Close()
			}
			return nil, nil, err
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			for _, l := range listeners {
				_ = l.Close()
			}
			return nil, nil, fmt.Errorf("proxy listen for %s: %w", ep, err)
		}
		listeners = append(listeners, ln)

		var keys []string
		keys = append(keys, "idx:"+strconv.Itoa(i), hostPort)
		if host, _, err := net.SplitHostPort(hostPort); err == nil {
			keys = append(keys, host)
		}
		if i < len(nodes) {
			keys = append(keys, nodes[i].Name, nodes[i].IP)
		}
		pn := p.nodeFor(keys...)

		go p.serveListener(ctx, ln, hostPort, pn)

		localAddr := ln.Addr().String()
		if scheme != "" {
			wrapped[i] = scheme + "://" + localAddr + pathSuffix
		} else {
			wrapped[i] = localAddr
		}
	}

	p.mu.Lock()
	p.listeners = append(p.listeners, listeners...)
	p.mu.Unlock()

	closeFn := func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}
	return wrapped, closeFn, nil
}

func splitEndpoint(ep string) (scheme, hostPort, pathSuffix string, err error) {
	if strings.Contains(ep, "://") {
		u, err := url.Parse(ep)
		if err != nil {
			return "", "", "", fmt.Errorf("invalid endpoint URL %q: %w", ep, err)
		}
		return u.Scheme, u.Host, u.Path, nil
	}
	return "", ep, "", nil
}

func (p *Proxy) serveListener(ctx context.Context, ln net.Listener, upstream string, pn *proxyNode) {
	for {
		clientConn, err := ln.Accept()
		if err != nil {
			return
		}
		go p.handleConn(ctx, clientConn, upstream, pn)
	}
}

func (p *Proxy) handleConn(ctx context.Context, clientConn net.Conn, upstream string, pn *proxyNode) {
	defer clientConn.Close()

	dropped, delay, loss := pn.waitReady()
	if dropped || (loss > 0 && rand.Float64() < loss) {
		return
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}

	var d net.Dialer
	d.Timeout = 5 * time.Second
	upConn, err := d.DialContext(ctx, "tcp", upstream)
	if err != nil {
		return
	}
	defer upConn.Close()

	if !pn.registerConn(clientConn) {
		return
	}
	defer pn.unregisterConn(clientConn)
	if !pn.registerConn(upConn) {
		return
	}
	defer pn.unregisterConn(upConn)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		p.pipe(ctx, upConn, clientConn, pn)
		_ = upConn.Close()
	}()
	go func() {
		defer wg.Done()
		p.pipe(ctx, clientConn, upConn, pn)
		_ = clientConn.Close()
	}()
	wg.Wait()
}

func (p *Proxy) pipe(ctx context.Context, dst, src net.Conn, pn *proxyNode) {
	buf := make([]byte, 32*1024)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			dropped, delay, loss := pn.waitReady()
			if dropped || (loss > 0 && rand.Float64() < loss) {
				_ = dst.Close()
				_ = src.Close()
				return
			}
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return
				}
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				_ = dst.Close()
			}
			return
		}
	}
}

func (p *Proxy) resolveNode(n Node, env nemesis.Env) (*proxyNode, int) {
	nodeIdx := -1
	for idx, target := range env.Nodes {
		if (n.Name != "" && target.Name == n.Name) || (n.IP != "" && target.IP == n.IP) {
			nodeIdx = idx
			break
		}
	}
	var keys []string
	if nodeIdx >= 0 {
		keys = append(keys, "idx:"+strconv.Itoa(nodeIdx))
	}
	keys = append(keys, n.Name, n.IP)
	return p.nodeFor(keys...), nodeIdx
}

// InjectFault implements FaultApplier in user space.
func (p *Proxy) InjectFault(_ context.Context, n Node, f nemesis.Fault, env nemesis.Env) (string, error) {
	t0 := time.Now()
	pn, nodeIdx := p.resolveNode(n, env)

	// When Injector dispatches a Partition across all cluster nodes, only
	// isolate the nodes belonging to f.Nodes so the remaining majority stays
	// reachable from clients.
	if f.Kind == nemesis.Partition && nodeIdx >= 0 && !slices.Contains(f.Nodes, nodeIdx) {
		return "proxy:healthy-side", nil
	}

	pn.applyFault(f)
	dur := time.Since(t0)
	p.collector.Record(agent.LatencySample{
		FaultID:   f.ID,
		Kind:      f.Kind,
		Node:      n.Name,
		Start:     true,
		RoundTrip: dur,
		Server:    dur,
		Method:    "proxy:l4",
	})
	return "proxy:l4", nil
}

// HealFault implements FaultApplier in user space.
func (p *Proxy) HealFault(_ context.Context, n Node, f nemesis.Fault, env nemesis.Env) (string, error) {
	t0 := time.Now()
	pn, _ := p.resolveNode(n, env)
	pn.healFault(f.ID)
	dur := time.Since(t0)
	p.collector.Record(agent.LatencySample{
		FaultID:   f.ID,
		Kind:      f.Kind,
		Node:      n.Name,
		Start:     false,
		RoundTrip: dur,
		Server:    dur,
		Method:    "proxy:l4",
	})
	return "proxy:l4", nil
}

// TeardownNode resets all active user-space faults on node n.
func (p *Proxy) TeardownNode(_ context.Context, n Node) error {
	pn := p.nodeFor(n.Name, n.IP)
	pn.reset()
	return nil
}

// LatencySummary implements FaultApplier.
func (p *Proxy) LatencySummary() stats.Summary { return p.collector.Summary() }
