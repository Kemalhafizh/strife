package probe

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CollectorConfig tunes the server-side diagnostic probe collector.
type CollectorConfig struct {
	Node               string
	Epoch              time.Time
	ProcRoot           string        // default "/proc"
	CgroupDir          string        // optional cgroup v2 directory containing memory.events
	CanaryDir          string        // directory for active fsync latency canary ("" to disable)
	FsyncThreshold     time.Duration // default 50ms
	PSIThreshold       time.Duration // default 20ms
	ClockJumpThreshold time.Duration // default 20ms
}

// Collector samples kernel PSI stall counters, cgroup OOM counters, active
// fsync canary latency, process signals, and wall-clock jumps on a node.
type Collector struct {
	cfg       CollectorConfig
	startTime time.Time

	mu          sync.Mutex
	events      []Event
	prevIOSome  uint64
	prevIOFull  uint64
	hasIO       bool
	prevCPUSome uint64
	hasCPU      bool
	prevOOMKill uint64
	hasOOM      bool
	lastSample  time.Time
	lastWall    time.Time
}

// NewCollector initializes a new server-side diagnostic collector.
func NewCollector(cfg CollectorConfig) *Collector {
	if cfg.ProcRoot == "" {
		cfg.ProcRoot = "/proc"
	}
	if cfg.FsyncThreshold <= 0 {
		cfg.FsyncThreshold = 50 * time.Millisecond
	}
	if cfg.PSIThreshold <= 0 {
		cfg.PSIThreshold = 20 * time.Millisecond
	}
	if cfg.ClockJumpThreshold <= 0 {
		cfg.ClockJumpThreshold = 20 * time.Millisecond
	}
	now := cfg.Epoch
	if now.IsZero() {
		now = time.Now()
	}
	return &Collector{
		cfg:        cfg,
		startTime:  now,
		lastSample: now,
		lastWall:   now.Round(0),
	}
}

// NowNs returns the elapsed nanoseconds since the collector was created or reset.
func (c *Collector) NowNs() int64 {
	return int64(time.Since(c.startTime))
}

// Record appends a server-side diagnostic event to the collector buffer.
func (c *Collector) Record(ev Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ev.Node == "" {
		ev.Node = c.cfg.Node
	}
	if ev.Start == 0 && ev.AtNs > 0 {
		ev.Start = ev.AtNs
	}
	if ev.Start == 0 && ev.End == 0 {
		now := int64(time.Since(c.startTime))
		if ev.DurationNs > 0 {
			ev.End = now
			ev.Start = now - ev.DurationNs
		} else {
			ev.Start = now
			ev.End = now
		}
	}
	if ev.AtNs == 0 {
		ev.AtNs = ev.Start
	}
	if ev.DurationNs == 0 && ev.End > ev.Start {
		ev.DurationNs = ev.End - ev.Start
	}
	if ev.ID == 0 {
		ev.ID = len(c.events) + 1
	}
	c.events = append(c.events, ev)
}

// RecordSignal records a process or cgroup signal delivery event.
func (c *Collector) RecordSignal(process string, pid int, signal, detail string) {
	c.Record(Event{
		Kind:    KindProcessSignal,
		Process: process,
		PID:     pid,
		Signal:  signal,
		Detail:  detail,
	})
}

// RecordFsyncStall records an observed fsync latency spike.
func (c *Collector) RecordFsyncStall(process, path string, dur time.Duration) {
	c.Record(Event{
		Kind:       KindFsyncStall,
		Process:    process,
		DurationNs: int64(dur),
		Detail:     fmt.Sprintf("fsync(%s) stalled for %v", path, dur.Round(time.Millisecond)),
	})
}

// Events returns a snapshot of all recorded probe events.
func (c *Collector) Events() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Event, len(c.events))
	copy(out, c.events)
	return out
}

// Reset clears recorded events and resets the relative time origin to now.
func (c *Collector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.startTime = now
	c.lastSample = now
	c.lastWall = now.Round(0)
	c.events = nil
}

// SampleOnce polls PSI, cgroup OOM counters, wall-clock drift, and optional
// fsync canary latency once.
func (c *Collector) SampleOnce() {
	c.sampleClock()
	c.samplePSI()
	c.sampleCgroupOOM()
	if c.cfg.CanaryDir != "" {
		c.sampleFsync(c.cfg.CanaryDir)
	}
}

func (c *Collector) sampleClock() {
	c.mu.Lock()
	now := time.Now()
	monoDelta := now.Sub(c.lastSample)
	wallNow := now.Round(0)
	wallDelta := wallNow.Sub(c.lastWall)
	c.lastSample = now
	c.lastWall = wallNow
	c.mu.Unlock()

	skew := wallDelta - monoDelta
	absSkew := skew
	if absSkew < 0 {
		absSkew = -absSkew
	}
	if absSkew >= c.cfg.ClockJumpThreshold {
		at := c.NowNs()
		c.Record(Event{
			Node:       c.cfg.Node,
			Kind:       KindClockJump,
			Start:      at,
			End:        at,
			DurationNs: int64(absSkew),
			Detail:     fmt.Sprintf("wall-clock jump %+v relative to monotonic clock", skew.Round(time.Millisecond)),
		})
	}
}

func (c *Collector) samplePSI() {
	nowNs := c.NowNs()

	ioPath := filepath.Join(c.cfg.ProcRoot, "pressure", "io")
	if data, err := os.ReadFile(ioPath); err == nil {
		if someUs, fullUs, ok := ParsePSITotal(string(data)); ok {
			c.mu.Lock()
			hadPrev := c.hasIO
			prevSome, prevFull := c.prevIOSome, c.prevIOFull
			c.prevIOSome, c.prevIOFull, c.hasIO = someUs, fullUs, true
			c.mu.Unlock()

			if hadPrev {
				var stallUs uint64
				kindStr := "full"
				if fullUs > prevFull {
					stallUs = fullUs - prevFull
				} else if someUs > prevSome {
					stallUs = someUs - prevSome
					kindStr = "some"
				}
				dur := time.Duration(stallUs) * time.Microsecond
				if dur >= c.cfg.PSIThreshold {
					c.Record(Event{
						Node:       c.cfg.Node,
						Kind:       KindIOPressure,
						Start:      nowNs - int64(dur),
						End:        nowNs,
						DurationNs: int64(dur),
						Detail:     fmt.Sprintf("/proc/pressure/io %s stalled %v", kindStr, dur.Round(time.Millisecond)),
					})
				}
			}
		}
	}

	cpuPath := filepath.Join(c.cfg.ProcRoot, "pressure", "cpu")
	if data, err := os.ReadFile(cpuPath); err == nil {
		if someUs, _, ok := ParsePSITotal(string(data)); ok {
			c.mu.Lock()
			hadPrev := c.hasCPU
			prevSome := c.prevCPUSome
			c.prevCPUSome, c.hasCPU = someUs, true
			c.mu.Unlock()

			if hadPrev && someUs > prevSome {
				dur := time.Duration(someUs-prevSome) * time.Microsecond
				if dur >= c.cfg.PSIThreshold {
					c.Record(Event{
						Node:       c.cfg.Node,
						Kind:       KindCPUPressure,
						Start:      nowNs - int64(dur),
						End:        nowNs,
						DurationNs: int64(dur),
						Detail:     fmt.Sprintf("/proc/pressure/cpu some stalled %v", dur.Round(time.Millisecond)),
					})
				}
			}
		}
	}
}

func (c *Collector) sampleCgroupOOM() {
	if c.cfg.CgroupDir == "" {
		return
	}
	memEvents := filepath.Join(c.cfg.CgroupDir, "memory.events")
	data, err := os.ReadFile(memEvents)
	if err != nil {
		return
	}
	kills, ok := ParseCgroupOOMKills(string(data))
	if !ok {
		return
	}
	c.mu.Lock()
	hadPrev := c.hasOOM
	prevKills := c.prevOOMKill
	c.prevOOMKill, c.hasOOM = kills, true
	c.mu.Unlock()

	if hadPrev && kills > prevKills {
		at := c.NowNs()
		c.Record(Event{
			Node:   c.cfg.Node,
			Kind:   KindOOMKill,
			Start:  at,
			End:    at,
			Detail: fmt.Sprintf("cgroup memory.events oom_kill increased by %d (total %d)", kills-prevKills, kills),
		})
	}
}

func (c *Collector) sampleFsync(dir string) {
	canaryPath := filepath.Join(dir, ".strife-fsync-canary.tmp")
	t0 := c.NowNs()
	start := time.Now()
	f, err := os.OpenFile(canaryPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(canaryPath)
	}()

	var page [512]byte
	if _, err := f.Write(page[:]); err != nil {
		at := c.NowNs()
		c.Record(Event{
			Node:   c.cfg.Node,
			Kind:   KindFsyncError,
			Start:  t0,
			End:    at,
			Errno:  "EIO",
			Detail: fmt.Sprintf("write canary failed: %v", err),
		})
		return
	}
	err = f.Sync()
	dur := time.Since(start)
	t1 := t0 + int64(dur)
	if err != nil {
		c.Record(Event{
			Node:       c.cfg.Node,
			Kind:       KindFsyncError,
			Start:      t0,
			End:        t1,
			DurationNs: int64(dur),
			Errno:      "EIO",
			Detail:     fmt.Sprintf("fsync failed: %v", err),
		})
		return
	}
	if dur >= c.cfg.FsyncThreshold {
		c.Record(Event{
			Node:       c.cfg.Node,
			Kind:       KindFsyncStall,
			Start:      t0,
			End:        t1,
			DurationNs: int64(dur),
			Detail:     fmt.Sprintf("fsync canary in %s took %v", dir, dur.Round(time.Millisecond)),
		})
	}
}

// ParsePSITotal extracts the "some" and "full" total stall microseconds from a
// Linux /proc/pressure/{io,cpu,memory} file.
func ParsePSITotal(content string) (someUs, fullUs uint64, ok bool) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kind := fields[0]
		for _, tok := range fields[1:] {
			if k, v, found := strings.Cut(tok, "="); found && k == "total" {
				if val, err := strconv.ParseUint(v, 10, 64); err == nil {
					if kind == "some" {
						someUs = val
						ok = true
					} else if kind == "full" {
						fullUs = val
						ok = true
					}
				}
			}
		}
	}
	return someUs, fullUs, ok
}

// ParseCgroupOOMKills extracts the oom_kill counter from cgroup v2 memory.events.
func ParseCgroupOOMKills(content string) (uint64, bool) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 2 && fields[0] == "oom_kill" {
			if val, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				return val, true
			}
		}
	}
	return 0, false
}
