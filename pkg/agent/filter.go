package agent

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// FilterEngine manages packet filtering and network partition rules.
type FilterEngine struct {
	mu      sync.Mutex
	dropped map[string]bool
	chain   string
	enabled bool
}

// NewFilterEngine creates a new packet filter manager.
func NewFilterEngine() *FilterEngine {
	return &FilterEngine{
		dropped: make(map[string]bool),
		chain:   nemesis.Chain,
	}
}

// Setup ensures the dedicated STRIFE chain exists and is hooked into INPUT.
func (f *FilterEngine) Setup() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Ensure iptables chain exists
	if _, err := exec.LookPath("iptables"); err != nil {
		return nil // iptables not available in environment
	}

	cmd := exec.Command("sh", "-c", fmt.Sprintf(
		"iptables -N %[1]s 2>/dev/null || true; iptables -C INPUT -j %[1]s 2>/dev/null || iptables -I INPUT -j %[1]s",
		f.chain,
	))
	if err := cmd.Run(); err != nil {
		return err
	}
	f.enabled = true
	return nil
}

// Drop drops all packets from source IP.
func (f *FilterEngine) Drop(ip string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.dropped[ip] {
		return nil // Already dropped (idempotent)
	}

	if f.enabled {
		cmd := exec.Command("iptables", "-A", f.chain, "-s", ip, "-j", "DROP")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("iptables drop %s: %w (%s)", ip, err, strings.TrimSpace(string(out)))
		}
	}
	f.dropped[ip] = true
	return nil
}

// Undrop removes the drop rule for source IP.
func (f *FilterEngine) Undrop(ip string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.dropped[ip] {
		return nil // Not dropped
	}

	if f.enabled {
		cmd := exec.Command("iptables", "-D", f.chain, "-s", ip, "-j", "DROP")
		_ = cmd.Run()
	}
	delete(f.dropped, ip)
	return nil
}

// Flush removes all active drop rules and clears the STRIFE chain.
func (f *FilterEngine) Flush() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.enabled {
		cmd := exec.Command("iptables", "-F", f.chain)
		_ = cmd.Run()
	}
	f.dropped = make(map[string]bool)
	return nil
}

// Dropped returns the list of currently dropped IPs.
func (f *FilterEngine) Dropped() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]string, 0, len(f.dropped))
	for ip := range f.dropped {
		out = append(out, ip)
	}
	return out
}
