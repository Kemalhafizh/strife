package nemesis

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Target describes a real node for command generation.
type Target struct {
	Name string
	IP   string
	// Cgroup is the absolute cgroup v2 directory of the database process on
	// the machine that hosts the node, e.g.
	// /sys/fs/cgroup/system.slice/docker-<id>.scope. Needed by freeze and
	// io-stall.
	Cgroup string
}

// Env holds what is needed to turn faults into shell commands.
type Env struct {
	Nodes     []Target
	Interface string // network interface for netem, default eth0
	Process   string // pkill -f pattern matching the database process
	Start     string // shell command that (re)starts the database in the background
	IODevice  string // "major:minor" of the data disk, for io-stall
}

// Command is a shell script to run for one node. Host commands run on the
// machine that hosts the node (for containers: the docker host) instead of
// inside the node itself.
type Command struct {
	Node   int
	Host   bool
	Script string
}

// Chain is the iptables chain Strife owns. Healing flushes only this chain,
// never the user's own rules.
const Chain = "STRIFE"

var (
	safeArg  = regexp.MustCompile(`^[A-Za-z0-9_.:/@%+=,-]+$`)
	ioDevice = regexp.MustCompile(`^[0-9]+:[0-9]+$`)
)

// Validate checks that the environment supports the requested kinds and that
// interpolated values cannot break out of the generated shell.
func (e Env) Validate(kinds []Kind) error {
	if len(e.Nodes) == 0 {
		return fmt.Errorf("nemesis: no nodes")
	}
	for i, n := range e.Nodes {
		if net.ParseIP(n.IP) == nil {
			return fmt.Errorf("nemesis: node %d (%s) has invalid ip %q", i, n.Name, n.IP)
		}
		if n.Cgroup != "" && !safeArg.MatchString(n.Cgroup) {
			return fmt.Errorf("nemesis: node %s has unsafe cgroup path %q", n.Name, n.Cgroup)
		}
	}
	if e.iface() != "" && !safeArg.MatchString(e.iface()) {
		return fmt.Errorf("nemesis: unsafe interface name %q", e.Interface)
	}
	for _, k := range kinds {
		switch k {
		case Kill, Pause:
			if e.Process == "" {
				return fmt.Errorf("nemesis: %s needs db.process (a pkill -f pattern)", k)
			}
			if k == Kill && e.Start == "" {
				return fmt.Errorf("nemesis: kill needs db.start to restart the process")
			}
		case Freeze, IOStall:
			for _, n := range e.Nodes {
				if n.Cgroup == "" {
					return fmt.Errorf("nemesis: %s needs a cgroup path for node %s", k, n.Name)
				}
			}
			if k == IOStall && !ioDevice.MatchString(e.IODevice) && e.IODevice != "auto" {
				return fmt.Errorf("nemesis: io-stall needs nemesis.io_device as major:minor (or \"auto\" for the transport or agent to find), got %q", e.IODevice)
			}
		}
	}
	return nil
}

func (e Env) iface() string {
	if e.Interface == "" {
		return "eth0"
	}
	return e.Interface
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// pgrepPattern quotes a pkill -f pattern so it cannot match the very shell
// running pkill (whose command line contains the pattern): "etcd --name"
// becomes the regex "[e]tcd --name", which matches etcd but not itself.
func (e Env) pgrepPattern() string {
	p := e.Process
	if p != "" && (p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z' || p[0] >= '0' && p[0] <= '9') {
		p = "[" + p[:1] + "]" + p[1:]
	}
	return quote(p)
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

// Setup prepares every node for the given kinds: a dedicated iptables chain
// hooked into INPUT when partitions are planned. Other kinds need no
// preparation, so nodes without iptables can still take them.
func (e Env) Setup(kinds []Kind) []Command {
	if !slices.Contains(kinds, Partition) {
		return nil
	}
	var out []Command
	for i := range e.Nodes {
		out = append(out, Command{Node: i, Script: fmt.Sprintf(
			"iptables -N %[1]s 2>/dev/null || true; iptables -C INPUT -j %[1]s 2>/dev/null || iptables -I INPUT -j %[1]s", Chain)})
	}
	return out
}

// Inject returns the commands that start fault f.
func (e Env) Inject(f Fault) []Command {
	var out []Command
	switch f.Kind {
	case Partition:
		a, b := f.Nodes, f.Other(len(e.Nodes))
		out = append(out, e.drop(a, b)...)
		out = append(out, e.drop(b, a)...)
	case Latency:
		for _, n := range f.Nodes {
			out = append(out, Command{Node: n, Script: fmt.Sprintf(
				"tc qdisc replace dev %s root netem delay %dms %dms distribution normal", e.iface(), ms(f.Delay), ms(f.Jitter))})
		}
	case Loss:
		for _, n := range f.Nodes {
			out = append(out, Command{Node: n, Script: fmt.Sprintf(
				"tc qdisc replace dev %s root netem loss %.1f%%", e.iface(), f.Loss*100)})
		}
	case Kill:
		for _, n := range f.Nodes {
			out = append(out, Command{Node: n, Script: "pkill -9 -f " + e.pgrepPattern() + " || true"})
		}
	case Pause:
		for _, n := range f.Nodes {
			out = append(out, Command{Node: n, Script: "pkill -STOP -f " + e.pgrepPattern() + " || true"})
		}
	case Freeze:
		for _, n := range f.Nodes {
			out = append(out, Command{Node: n, Host: true, Script: freezeScript(e.Nodes[n].Cgroup, 1)})
		}
	case IOStall:
		for _, n := range f.Nodes {
			out = append(out, Command{Node: n, Host: true, Script: fmt.Sprintf(
				"echo '%s rbps=1 wbps=1 riops=1 wiops=1' > %s/io.max", e.IODevice, e.Nodes[n].Cgroup)})
		}
	case ClockSkew:
		for _, n := range f.Nodes {
			out = append(out, Command{Node: n, Script: fmt.Sprintf(
				`date -s "@$(( $(date +%%s) + %d ))" >/dev/null`, int64(f.Offset/time.Second))})
		}
	}
	return out
}

// freezeScript requests a freezer state and waits (up to 5s) until the
// kernel reports it in cgroup.events: freezing is asynchronous, and a fault
// must not be reported as applied before it is in effect.
func freezeScript(cgroup string, state int) string {
	return fmt.Sprintf(`echo %[2]d > %[1]s/cgroup.freeze || exit 1; i=0; while ! grep -qx 'frozen %[2]d' %[1]s/cgroup.events; do i=$((i+1)); [ $i -gt 500 ] && { echo 'cgroup %[1]s did not reach frozen=%[2]d within 5s' >&2; exit 1; }; sleep 0.01; done`, cgroup, state)
}

func (e Env) drop(from, to []int) []Command {
	var out []Command
	for _, n := range from {
		var rules []string
		for _, m := range to {
			rules = append(rules, fmt.Sprintf("iptables -A %s -s %s -j DROP", Chain, e.Nodes[m].IP))
		}
		if len(rules) > 0 {
			out = append(out, Command{Node: n, Script: strings.Join(rules, " && ")})
		}
	}
	return out
}

// Heal returns the commands that end fault f.
func (e Env) Heal(f Fault) []Command {
	var out []Command
	switch f.Kind {
	case Partition:
		for i := range e.Nodes {
			out = append(out, Command{Node: i, Script: "iptables -F " + Chain})
		}
	case Latency, Loss:
		for _, n := range f.Nodes {
			out = append(out, Command{Node: n, Script: fmt.Sprintf("tc qdisc del dev %s root 2>/dev/null || true", e.iface())})
		}
	case Kill:
		for _, n := range f.Nodes {
			out = append(out, Command{Node: n, Script: e.Start})
		}
	case Pause:
		for _, n := range f.Nodes {
			out = append(out, Command{Node: n, Script: "pkill -CONT -f " + e.pgrepPattern() + " || true"})
		}
	case Freeze:
		for _, n := range f.Nodes {
			out = append(out, Command{Node: n, Host: true, Script: freezeScript(e.Nodes[n].Cgroup, 0)})
		}
	case IOStall:
		for _, n := range f.Nodes {
			out = append(out, Command{Node: n, Host: true, Script: fmt.Sprintf(
				"echo '%s rbps=max wbps=max riops=max wiops=max' > %s/io.max", e.IODevice, e.Nodes[n].Cgroup)})
		}
	case ClockSkew:
		for _, n := range f.Nodes {
			out = append(out, Command{Node: n, Script: fmt.Sprintf(
				`date -s "@$(( $(date +%%s) - %d ))" >/dev/null`, int64(f.Offset/time.Second))})
		}
	}
	return out
}

// Teardown best-effort heals everything Strife may have touched, for every
// kind in use. It is safe to run more than once.
func (e Env) Teardown(kinds []Kind) []Command {
	has := map[Kind]bool{}
	for _, k := range kinds {
		has[k] = true
	}
	var out []Command
	for i, n := range e.Nodes {
		var parts []string
		if has[Partition] {
			parts = append(parts, "iptables -F "+Chain+" 2>/dev/null || true")
		}
		if has[Latency] || has[Loss] {
			parts = append(parts, fmt.Sprintf("tc qdisc del dev %s root 2>/dev/null || true", e.iface()))
		}
		if has[Pause] {
			parts = append(parts, "pkill -CONT -f "+e.pgrepPattern()+" || true")
		}
		if has[Kill] {
			// Interrupted mid-kill: bring the database back, but never twice.
			parts = append(parts, "pgrep -f "+e.pgrepPattern()+" >/dev/null || ("+e.Start+")")
		}
		if len(parts) > 0 {
			out = append(out, Command{Node: i, Script: strings.Join(parts, "; ")})
		}
		if has[Freeze] {
			out = append(out, Command{Node: i, Host: true, Script: fmt.Sprintf("echo 0 > %s/cgroup.freeze || true", n.Cgroup)})
		}
		if has[IOStall] {
			out = append(out, Command{Node: i, Host: true, Script: fmt.Sprintf(
				"echo '%s rbps=max wbps=max riops=max wiops=max' > %s/io.max || true", e.IODevice, n.Cgroup)})
		}
	}
	return out
}
