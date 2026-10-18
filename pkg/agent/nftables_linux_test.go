//go:build linux

package agent

import (
	"net"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// inFreshNetns runs fn on a thread moved into a fresh network namespace, so the
// test can program nf_tables without touching the machine's own rules. It
// needs CAP_SYS_ADMIN and CAP_NET_ADMIN, and skips without them.
// fn may only call t.Error (it runs on another goroutine); it returns a
// reason to skip, or "".
func inFreshNetns(t *testing.T, fn func() string) {
	t.Helper()
	skip := make(chan string, 1)
	go func() {
		runtime.LockOSThread() // never unlocked: the thread dies with the goroutine
		if err := syscall.Unshare(syscall.CLONE_NEWNET); err != nil {
			skip <- "cannot create a network namespace: " + err.Error()
			return
		}
		if err := loopbackUp(); err != nil {
			skip <- "cannot bring up lo: " + err.Error()
			return
		}
		skip <- fn()
	}()
	if reason := <-skip; reason != "" {
		t.Skip(reason)
	}
}

func loopbackUp() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var req struct {
		name  [16]byte
		flags uint16
		_     [22]byte
	}
	copy(req.name[:], "lo")
	req.flags = syscall.IFF_UP | syscall.IFF_LOOPBACK | syscall.IFF_RUNNING
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&req))); e != 0 {
		return e
	}
	return nil
}

func reachable(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err == nil {
		c.Close()
	}
	return err == nil
}

func TestNFTablesReallyDrops(t *testing.T) {
	inFreshNetns(t, func() string {
		f := &nftablesFilter{}
		if !f.Available() {
			return "nf_tables not available"
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Error(err)
			return ""
		}
		defer l.Close()
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		addr := l.Addr().String()
		if !reachable(addr) {
			t.Error("listener unreachable before any rule")
			return ""
		}
		if err := f.Sync([]net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("fd00::7")}); err != nil {
			t.Error(err)
			return ""
		}
		if reachable(addr) {
			t.Error("127.0.0.1 still reachable with a drop rule installed")
			return ""
		}
		if out, err := exec.Command("nft", "list", "ruleset").CombinedOutput(); err == nil {
			for _, want := range []string{"table ip strife", "hook input priority filter", "ip saddr 127.0.0.1 drop", "table ip6 strife", "ip6 saddr fd00::7 drop"} {
				if !strings.Contains(string(out), want) {
					t.Errorf("nft does not show %q:\n%s", want, out)
				}
			}
		} else {
			t.Logf("nft not available to cross-check the ruleset: %v", err)
		}
		ruleset := func() string {
			out, _ := exec.Command("nft", "list", "ruleset").CombinedOutput()
			return string(out)
		}
		// Heal: an accept rule at the head overrides the drop, instantly.
		if err := f.Sync(nil); err != nil {
			t.Error(err)
			return ""
		}
		if !reachable(addr) {
			t.Error("still blocked after the partition healed")
			return ""
		}
		// Block again: the newest rule (drop, at the head) wins.
		if err := f.Sync([]net.IP{net.ParseIP("127.0.0.1")}); err != nil {
			t.Error(err)
			return ""
		}
		if reachable(addr) {
			t.Error("not blocked again after re-injecting")
			return ""
		}
		// Compaction leaves one drop rule per blocked source and no
		// leftover accept, without changing what is blocked.
		if err := f.Compact(); err != nil {
			t.Error(err)
			return ""
		}
		if reachable(addr) {
			t.Error("compaction unblocked a source")
			return ""
		}
		if rs := ruleset(); rs != "" && (strings.Count(rs, "saddr 127.0.0.1") != 1 || strings.Contains(rs, "accept\n\t\t") || strings.Contains(rs, "fd00::7")) {
			t.Errorf("chain not minimal after compaction:\n%s", rs)
		}
		if err := f.Sync([]net.IP{net.ParseIP("10.9.9.9")}); err != nil {
			t.Error(err)
			return ""
		}
		if !reachable(addr) {
			t.Error("replacing the blocked set kept the old source blocked")
			return ""
		}
		if err := f.Teardown(); err != nil {
			t.Error(err)
			return ""
		}
		if err := f.Teardown(); err != nil {
			t.Errorf("a second teardown must be harmless: %v", err)
			return ""
		}
		if out, err := exec.Command("nft", "list", "ruleset").CombinedOutput(); err == nil && strings.Contains(string(out), "strife") {
			t.Errorf("tables left behind:\n%s", out)
		}
		return ""
	})
}

// BenchmarkPartitionBackends compares one partition change (block two
// sources, then lift them) through nf_tables netlink and through the
// iptables binary, inside a private network namespace.
func BenchmarkPartitionBackends(b *testing.B) {
	for _, be := range []filterBackend{&nftablesFilter{}, &iptablesFilter{chain: "STRIFE"}} {
		b.Run(be.Name(), func(b *testing.B) {
			done := make(chan string, 1)
			go func() {
				runtime.LockOSThread()
				if err := syscall.Unshare(syscall.CLONE_NEWNET); err != nil {
					done <- err.Error()
					return
				}
				if !be.Available() {
					done <- be.Name() + " not available"
					return
				}
				// Each transition goes through the engine, as in the agent,
				// so compaction runs when changes go quiet.
				e := &FilterEngine{dropped: map[string]net.IP{}, backends: []filterBackend{be}}
				if err := e.Setup(); err != nil {
					done <- err.Error()
					return
				}
				var inject, heal []time.Duration
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					t0 := time.Now()
					if err := e.Drop("10.0.0.2", "10.0.0.3"); err != nil {
						done <- err.Error()
						return
					}
					t1 := time.Now()
					if err := e.Flush(); err != nil {
						done <- err.Error()
						return
					}
					inject, heal = append(inject, t1.Sub(t0)), append(heal, time.Since(t1))
					if i%10 == 9 {
						time.Sleep(compactAfter + 30*time.Millisecond) // a quiet period, as between faults
					}
				}
				b.StopTimer()
				median := func(d []time.Duration) float64 {
					slices.Sort(d)
					return float64(d[len(d)/2].Microseconds())
				}
				b.ReportMetric(median(inject), "inject-p50-µs")
				b.ReportMetric(median(heal), "heal-p50-µs")
				_ = be.Teardown()
				done <- ""
			}()
			if reason := <-done; reason != "" {
				b.Skip(reason)
			}
		})
	}
}
