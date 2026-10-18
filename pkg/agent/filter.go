package agent

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// filterBackend installs the set of blocked source addresses.
type filterBackend interface {
	Name() string
	Available() bool
	// Sync makes the node drop exactly these sources, replacing whatever
	// was blocked before.
	Sync(ips []net.IP) error
	// Compact tidies the backend's rules without changing what is
	// blocked. It may be slow; the engine runs it when changes go quiet.
	Compact() error
	// Teardown removes everything the backend installed.
	Teardown() error
}

// compactAfter is how long the filter must stay unchanged before the
// engine compacts it.
var compactAfter = 250 * time.Millisecond

// ErrNoFilter means neither nftables nor iptables can be used on this node,
// so a partition cannot be applied: reporting it as applied would be a lie.
var ErrNoFilter = errors.New("agent: no packet filter available (need nftables with CAP_NET_ADMIN, or the iptables binary)")

// FilterEngine manages the sources a node drops during partitions. It
// prefers nf_tables over netlink and falls back to the iptables binary.
type FilterEngine struct {
	mu       sync.Mutex
	dropped  map[string]net.IP
	backend  filterBackend
	backends []filterBackend
	probed   bool // Setup ran: backend is the choice, nil if none works
	compact  *time.Timer
	// run, when set, runs each backend call: in another container's
	// network namespace, for an agent serving a whole node.
	run func(func() error) error
}

// do runs a backend call where this engine's rules live.
func (f *FilterEngine) do(fn func() error) error {
	if f.run == nil {
		return fn()
	}
	return f.run(fn)
}

// NewFilterEngine creates a packet filter manager; Setup picks the backend.
func NewFilterEngine() *FilterEngine {
	return &FilterEngine{
		dropped:  map[string]net.IP{},
		backends: []filterBackend{newNFTablesBackend(), &iptablesFilter{chain: nemesis.Chain}},
	}
}

// Setup selects the first usable backend. It is not an error for none to be
// usable: partitions then fail with ErrNoFilter, other faults still work.
// The engine calls it on first use, so an agent that never partitions never
// touches the node's packet filter.
func (f *FilterEngine) Setup() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.setup()
}

func (f *FilterEngine) setup() error {
	f.probed, f.backend = true, nil
	for _, b := range f.backends {
		ok := false
		if err := f.do(func() error { ok = b.Available(); return nil }); err != nil {
			f.probed = false // the namespace could not be entered: try again next time
			return err
		}
		if ok {
			f.backend = b
			return nil
		}
	}
	return ErrNoFilter
}

func (f *FilterEngine) ensure() {
	if !f.probed {
		_ = f.setup()
	}
}

// Method names the backend in use, e.g. "filter:nftables".
func (f *FilterEngine) Method() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensure()
	if f.backend == nil {
		return "filter:none"
	}
	return "filter:" + f.backend.Name()
}

func (f *FilterEngine) sync() error {
	f.ensure()
	if f.backend == nil {
		return ErrNoFilter
	}
	ips := make([]net.IP, 0, len(f.dropped))
	for _, ip := range f.dropped {
		ips = append(ips, ip)
	}
	sort.Slice(ips, func(i, j int) bool { return ips[i].String() < ips[j].String() })
	b := f.backend
	if err := f.do(func() error { return b.Sync(ips) }); err != nil {
		return err
	}
	if f.compact != nil {
		f.compact.Stop()
	}
	f.compact = time.AfterFunc(compactAfter, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if b := f.backend; b != nil {
			_ = f.do(b.Compact)
		}
	})
	return nil
}

// Drop starts dropping packets from every given source, in one change.
func (f *FilterEngine) Drop(ips ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	added := []string{}
	for _, s := range ips {
		ip := net.ParseIP(strings.TrimSpace(s))
		if ip == nil {
			return fmt.Errorf("agent: invalid IP %q", s)
		}
		if _, ok := f.dropped[ip.String()]; !ok {
			f.dropped[ip.String()] = ip
			added = append(added, ip.String())
		}
	}
	if len(added) == 0 {
		return nil // already in effect (idempotent)
	}
	if err := f.sync(); err != nil {
		for _, k := range added {
			delete(f.dropped, k)
		}
		return err
	}
	return nil
}

// Undrop stops dropping packets from the given sources.
func (f *FilterEngine) Undrop(ips ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	changed := false
	for _, s := range ips {
		if ip := net.ParseIP(strings.TrimSpace(s)); ip != nil {
			if _, ok := f.dropped[ip.String()]; ok {
				delete(f.dropped, ip.String())
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	return f.sync()
}

// Flush stops dropping anything.
func (f *FilterEngine) Flush() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.dropped) == 0 {
		return nil
	}
	f.dropped = map[string]net.IP{}
	return f.sync()
}

// Close removes everything the backend installed; the engine must be set
// up again before further use.
func (f *FilterEngine) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.compact != nil {
		f.compact.Stop()
	}
	f.dropped = map[string]net.IP{}
	if f.backend == nil {
		return nil
	}
	err := f.do(f.backend.Teardown)
	f.backend, f.probed = nil, false
	return err
}

// Dropped returns the sources currently dropped, sorted.
func (f *FilterEngine) Dropped() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.dropped))
	for k := range f.dropped {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// iptablesFilter shells out to iptables: one process per rule. It is the
// fallback for kernels or containers without nf_tables access.
type iptablesFilter struct {
	chain string
	bin   string
}

func (t *iptablesFilter) Name() string { return "iptables" }

func (t *iptablesFilter) run(args ...string) error {
	out, err := exec.Command(t.bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (t *iptablesFilter) Available() bool {
	if t.bin == "" {
		p, err := exec.LookPath("iptables")
		if err != nil {
			return false
		}
		t.bin = p
	}
	if t.run("-N", t.chain) != nil && t.run("-L", t.chain, "-n") != nil {
		return false
	}
	if t.run("-C", "INPUT", "-j", t.chain) != nil {
		return t.run("-I", "INPUT", "-j", t.chain) == nil
	}
	return true
}

func (t *iptablesFilter) Sync(ips []net.IP) error {
	if err := t.run("-F", t.chain); err != nil {
		return err
	}
	for _, ip := range ips {
		if ip.To4() == nil {
			return fmt.Errorf("agent: the iptables fallback handles IPv4 only; partitioning %s needs nftables", ip)
		}
		if err := t.run("-A", t.chain, "-s", ip.String(), "-j", "DROP"); err != nil {
			return err
		}
	}
	return nil
}

func (t *iptablesFilter) Compact() error { return nil }

func (t *iptablesFilter) Teardown() error { return t.run("-F", t.chain) }
