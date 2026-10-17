package agent

import (
	"errors"
	"net"
	"slices"
	"testing"
)

// fakeFilter records what the engine asked the kernel to block.
type fakeFilter struct {
	name      string
	usable    bool
	failSync  error
	syncs     [][]string
	torndown  int
	compacted int
}

func (f *fakeFilter) Name() string {
	if f.name == "" {
		return "fake"
	}
	return f.name
}

func (f *fakeFilter) Available() bool { return f.name == "" || f.usable }

func (f *fakeFilter) Sync(ips []net.IP) error {
	if f.failSync != nil {
		return f.failSync
	}
	var s []string
	for _, ip := range ips {
		s = append(s, ip.String())
	}
	f.syncs = append(f.syncs, s)
	return nil
}

func (f *fakeFilter) Compact() error  { f.compacted++; return nil }
func (f *fakeFilter) Teardown() error { f.torndown++; return nil }

func TestFilterEngineBatchesAndRollsBack(t *testing.T) {
	nft := &fakeFilter{name: "nftables", usable: true}
	e := &FilterEngine{dropped: map[string]net.IP{}, backends: []filterBackend{nft, &fakeFilter{name: "iptables", usable: true}}}
	// Nothing is set up until the first use.
	if e.probed {
		t.Fatal("backend probed before any use")
	}
	if e.Method() != "filter:nftables" {
		t.Fatalf("method %s", e.Method())
	}
	if err := e.Drop("10.0.0.3", "10.0.0.2", "10.0.0.2"); err != nil {
		t.Fatal(err)
	}
	if len(nft.syncs) != 1 || !slices.Equal(nft.syncs[0], []string{"10.0.0.2", "10.0.0.3"}) {
		t.Fatalf("one sorted, deduplicated change expected: %v", nft.syncs)
	}
	if err := e.Drop("10.0.0.2"); err != nil || len(nft.syncs) != 1 {
		t.Fatalf("an already applied drop must not touch the kernel: %v %v", err, nft.syncs)
	}
	if err := e.Undrop("10.0.0.3"); err != nil || !slices.Equal(nft.syncs[1], []string{"10.0.0.2"}) {
		t.Fatalf("undrop: %v %v", err, nft.syncs)
	}
	nft.failSync = errors.New("EPERM")
	if err := e.Drop("10.0.0.9"); err == nil || slices.Contains(e.Dropped(), "10.0.0.9") {
		t.Fatalf("a failed change must be rolled back: %v %v", err, e.Dropped())
	}
	if err := e.Drop("not-an-ip"); err == nil {
		t.Fatal("invalid IP accepted")
	}
	nft.failSync = nil
	if err := e.Flush(); err != nil || len(e.Dropped()) != 0 || len(nft.syncs[len(nft.syncs)-1]) != 0 {
		t.Fatalf("flush: %v %v %v", err, e.Dropped(), nft.syncs)
	}
	if err := e.Close(); err != nil || nft.torndown != 1 || len(e.Dropped()) != 0 {
		t.Fatalf("close: %v %d %v", err, nft.torndown, e.Dropped())
	}
	// The next use sets the backend up again.
	if err := e.Drop("10.0.0.4"); err != nil || !slices.Equal(nft.syncs[len(nft.syncs)-1], []string{"10.0.0.4"}) {
		t.Fatalf("drop after close: %v %v", err, nft.syncs)
	}
}

func TestFilterEngineFallsBackAndRefusesToPretend(t *testing.T) {
	ipt := &fakeFilter{name: "iptables", usable: true}
	e := &FilterEngine{dropped: map[string]net.IP{}, backends: []filterBackend{&fakeFilter{name: "nftables"}, ipt}}
	if err := e.Setup(); err != nil || e.Method() != "filter:iptables" {
		t.Fatalf("fallback: %v %s", err, e.Method())
	}
	none := &FilterEngine{dropped: map[string]net.IP{}, backends: []filterBackend{&fakeFilter{name: "nftables"}, &fakeFilter{name: "iptables"}}}
	if none.Method() != "filter:none" {
		t.Fatalf("method without any filter: %s", none.Method())
	}
	if err := none.Setup(); !errors.Is(err, ErrNoFilter) {
		t.Fatalf("setup without any filter: %v", err)
	}
	if err := none.Drop("10.0.0.2"); !errors.Is(err, ErrNoFilter) {
		t.Fatalf("a partition without a filter must fail, got %v", err)
	}
}
