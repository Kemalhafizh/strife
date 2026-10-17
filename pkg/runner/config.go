// Package runner executes a Strife test against a real cluster: it drives a
// concurrent workload through database clients while a nemesis injects the
// seeded fault schedule with iptables, tc, signals and cgroups, then checks
// the recorded history.
package runner

import (
	"encoding/json"
	"fmt"
	"github.com/kemalhafizh/strife/internal/secret"
	"os"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/client"
	"github.com/kemalhafizh/strife/pkg/model"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/transport"
	"github.com/kemalhafizh/strife/pkg/txn"
	"github.com/kemalhafizh/strife/pkg/workload"
)

// Duration is a time.Duration that reads and writes "10s" style JSON.
type Duration time.Duration

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"10s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Config is a live test description, usually loaded from strife.json.
type Config struct {
	Name      string           `json:"name"`
	Seed      uint64           `json:"seed"`
	Duration  Duration         `json:"duration"`
	Nodes     []transport.Node `json:"nodes"`
	Transport transport.Config `json:"transport"`
	DB        DBConfig         `json:"db"`
	Workload  WorkloadConfig   `json:"workload"`
	Nemesis   NemesisConfig    `json:"nemesis"`
	Checker   CheckerConfig    `json:"checker"`
}

// DBConfig describes the database under test.
type DBConfig struct {
	client.Config
	Process string `json:"process,omitempty"` // pkill -f pattern
	Start   string `json:"start,omitempty"`   // restart command (run inside the node)
}

// WorkloadConfig shapes the generated operations.
type WorkloadConfig struct {
	// Model is "kv" (single-key reads, writes and compare-and-sets, checked
	// for linearizability) or "append" (list-append transactions, checked
	// Elle-style at Isolation).
	Model       string   `json:"model"`
	Keys        int      `json:"keys"`        // independent registers
	Values      int      `json:"values"`      // values drawn from [0, values)
	Concurrency int      `json:"concurrency"` // client processes
	ReadRatio   float64  `json:"read_ratio"`
	CasRatio    float64  `json:"cas_ratio"`
	Think       Duration `json:"think"`   // max pause between ops per process
	Timeout     Duration `json:"timeout"` // per-operation timeout
	Plugin      string   `json:"plugin,omitempty"`
	// TxnLen is the maximum number of micro-operations per transaction
	// (append only; 1 makes every operation a single append or read, e.g.
	// for Kafka, where a key is a partition log).
	TxnLen int `json:"txn_len,omitempty"`
	// WritesPerKey retires a key after about this many appends, so lists,
	// and with them reads and checking, stay small (append only).
	WritesPerKey int `json:"writes_per_key,omitempty"`
	// Isolation is the level append histories are checked against:
	// read-uncommitted, read-committed, snapshot-isolation,
	// repeatable-read, serializable or strict-serializable.
	Isolation string `json:"isolation,omitempty"`
}

// NemesisConfig shapes the fault schedule.
type NemesisConfig struct {
	Faults    []nemesis.Kind `json:"faults"`
	Interval  Duration       `json:"interval"`
	Length    Duration       `json:"length"`
	Interface string         `json:"interface,omitempty"`
	IODevice  string         `json:"io_device,omitempty"`
}

// CheckerConfig tunes the linearizability search.
type CheckerConfig struct {
	MaxSteps uint64   `json:"max_steps,omitempty"`
	Timeout  Duration `json:"timeout,omitempty"` // wall-clock budget per key
}

// Load reads, defaults and validates a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Defaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Defaults fills unset fields.
func (c *Config) Defaults() {
	if c.Name == "" {
		c.Name = "strife"
	}
	if c.Duration == 0 {
		c.Duration = Duration(60 * time.Second)
	}
	w := &c.Workload
	if w.Model == "" {
		w.Model = "kv"
	}
	if w.Keys == 0 {
		w.Keys = 4
	}
	if w.Values == 0 {
		w.Values = 5
	}
	if w.Concurrency == 0 {
		w.Concurrency = 2 * max(1, len(c.Nodes))
	}
	if w.ReadRatio == 0 && w.CasRatio == 0 {
		w.ReadRatio, w.CasRatio = 0.5, 0.2
		if w.Model == "append" {
			w.CasRatio = 0
		}
	}
	if w.TxnLen == 0 {
		w.TxnLen = 4
	}
	if w.WritesPerKey == 0 {
		w.WritesPerKey = 32
	}
	if w.Isolation == "" {
		w.Isolation = "serializable"
	}
	if w.Think == 0 {
		w.Think = Duration(50 * time.Millisecond)
	}
	if w.Timeout == 0 {
		w.Timeout = Duration(2 * time.Second)
	}
	n := &c.Nemesis
	if n.Interval == 0 {
		n.Interval = Duration(time.Duration(c.Duration) / 10)
	}
	if n.Length == 0 {
		n.Length = n.Interval
	}
}

// Validate checks the config and that the fault kinds are supported by the
// environment.
func (c *Config) Validate() error {
	if len(c.Nodes) == 0 {
		return fmt.Errorf("at least one node is required")
	}
	switch c.Workload.Model {
	case "kv":
	case "append":
		if c.Workload.Plugin != "" {
			return fmt.Errorf("workload.plugin generates kv operations; it cannot drive the append model")
		}
		if c.Workload.TxnLen < 1 || c.Workload.WritesPerKey < 1 {
			return fmt.Errorf("workload.txn_len and writes_per_key must be positive")
		}
		switch txn.IsolationLevel(c.Workload.Isolation) {
		case txn.LevelReadUncommitted, txn.LevelReadCommitted, txn.LevelSnapshotIsolation, txn.LevelRepeatableRead, txn.LevelSerializable, txn.LevelStrictSerializable:
		default:
			return fmt.Errorf("workload.isolation %q is not a known level", c.Workload.Isolation)
		}
	default:
		return fmt.Errorf("workload.model %q not supported for live runs (kv, append)", c.Workload.Model)
	}
	if c.Workload.ReadRatio < 0 || c.Workload.CasRatio < 0 || c.Workload.ReadRatio+c.Workload.CasRatio > 1 {
		return fmt.Errorf("workload.read_ratio + cas_ratio must be within [0, 1]")
	}
	if c.Workload.Concurrency < 1 || c.Workload.Keys < 1 || c.Workload.Values < 1 {
		return fmt.Errorf("workload.concurrency, keys and values must be positive")
	}
	if c.Workload.Plugin != "" {
		if _, err := workload.Load(c.Workload.Plugin); err != nil {
			return err
		}
	}
	k8s := c.Transport.Type == "kubernetes" || c.Transport.Type == "k8s"
	for _, k := range c.Nemesis.Faults {
		if k == nemesis.ClockSkew && c.Transport.Type == "docker" {
			return fmt.Errorf("clock-skew is refused with the docker transport: containers share the host clock, so it would skew the docker host itself")
		}
		if k8s {
			switch k {
			case nemesis.ClockSkew:
				return fmt.Errorf("clock-skew is refused with the kubernetes transport: pods share their node's clock")
			case nemesis.Freeze, nemesis.IOStall:
				return fmt.Errorf("%s needs host access, which the kubernetes transport does not have: run strife-agent on the nodes (transport \"agent\")", k)
			}
		}
	}
	for _, n := range c.Nodes {
		if n.IP == transport.Auto && !k8s {
			return fmt.Errorf("node %s: \"ip\": \"auto\" needs the kubernetes transport", n.Name)
		}
		if k8s && n.Pod == "" {
			return fmt.Errorf("node %s: the kubernetes transport needs a pod", n.Name)
		}
	}
	if _, err := nemesis.ParseKinds(joinKinds(c.Nemesis.Faults)); err != nil {
		return err
	}
	env := c.Env()
	for i := range env.Nodes {
		if env.Nodes[i].IP == transport.Auto {
			env.Nodes[i].IP = "192.0.2.1" // resolved when the run starts
		}
	}
	return env.Validate(c.Nemesis.Faults)
}

func joinKinds(ks []nemesis.Kind) string {
	s := make([]string, len(ks))
	for i, k := range ks {
		s[i] = string(k)
	}
	return strings.Join(s, ",")
}

// Redacted returns a copy safe to embed in reports and attestations:
// literal passwords and tokens are masked (env:/file: references are kept).
func (c *Config) Redacted() *Config {
	r := *c
	r.DB.Config = c.DB.Config.Redacted()
	r.Transport.Token = secret.Redact(c.Transport.Token)
	return &r
}

// Model returns the consistency model the recorded history is checked with.
func (c *Config) Model() model.Model {
	if c.Workload.Model == "append" {
		return model.Append{Level: c.Workload.Isolation}
	}
	return model.KV{}
}

// Env converts the config into the nemesis command environment.
func (c *Config) Env() nemesis.Env {
	e := nemesis.Env{Interface: c.Nemesis.Interface, Process: c.DB.Process, Start: c.DB.Start, IODevice: c.Nemesis.IODevice}
	for _, n := range c.Nodes {
		e.Nodes = append(e.Nodes, nemesis.Target{Name: n.Name, IP: n.IP, Cgroup: n.Cgroup})
	}
	return e
}

// Schedule generates the deterministic fault schedule for this config.
func (c *Config) Schedule() nemesis.Schedule {
	return nemesis.Generate(c.Seed, nemesis.Options{
		Nodes:    len(c.Nodes),
		Duration: time.Duration(c.Duration),
		Kinds:    c.Nemesis.Faults,
		Interval: time.Duration(c.Nemesis.Interval),
		Length:   time.Duration(c.Nemesis.Length),
	})
}
