// Package campaign executes parallel multi-seed chaos simulation campaigns,
// tracks seed-by-seed regressions across commits, and renders self-contained
// campaign and regression dashboards.
package campaign

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/explore"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// SeedRecord captures the deterministic execution metrics and verdict of a single seed.
type SeedRecord struct {
	Seed          uint64          `json:"seed"`
	Verdict       checker.Verdict `json:"verdict"`
	Ops           int             `json:"ops"`
	Faults        int             `json:"faults"`
	MinimalFaults int             `json:"minimal_faults,omitempty"`
	Events        uint64          `json:"events"`
	TraceHash     string          `json:"trace_hash"`
	HistoryHash   string          `json:"history_hash"`
	ScheduleHash  string          `json:"schedule_hash"`
	Explanation   string          `json:"explanation,omitempty"`
	Reproduce     string          `json:"reproduce,omitempty"`
	ReportPath    string          `json:"report_path,omitempty"`
	Error         string          `json:"error,omitempty"`
	ElapsedMs     int64           `json:"elapsed_ms"`
}

// Manifest is the serializable ledger of a multi-seed campaign run.
type Manifest struct {
	Tool        string         `json:"tool"`
	Version     string         `json:"version"`
	CampaignID  string         `json:"campaign_id"`
	Fingerprint string         `json:"fingerprint"`
	Commit      string         `json:"commit,omitempty"`
	Branch      string         `json:"branch,omitempty"`
	Timestamp   string         `json:"timestamp"`
	System      string         `json:"system"`
	Faults      []nemesis.Kind `json:"faults"`
	Config      explore.Config `json:"config"`

	TotalSeeds  int    `json:"total_seeds"`
	Passed      int    `json:"passed"`
	Violations  int    `json:"violations"`
	Unknown     int    `json:"unknown"`
	Errors      int    `json:"errors"`
	TotalEvents uint64 `json:"total_events"`
	TotalOps    int    `json:"total_ops"`
	DurationMs  int64  `json:"duration_ms"`

	Seeds      []SeedRecord `json:"seeds"`
	Comparison *Comparison  `json:"comparison,omitempty"`
}

// ComputeFingerprint calculates a deterministic SHA-256 digest over the campaign's
// configuration and ordered per-seed hashes and verdicts.
func (m *Manifest) ComputeFingerprint() string {
	type fpSeed struct {
		Seed         uint64          `json:"seed"`
		Verdict      checker.Verdict `json:"verdict"`
		TraceHash    string          `json:"trace_hash"`
		HistoryHash  string          `json:"history_hash"`
		ScheduleHash string          `json:"schedule_hash"`
	}
	seeds := make([]fpSeed, len(m.Seeds))
	for i, s := range m.Seeds {
		seeds[i] = fpSeed{
			Seed:         s.Seed,
			Verdict:      s.Verdict,
			TraceHash:    s.TraceHash,
			HistoryHash:  s.HistoryHash,
			ScheduleHash: s.ScheduleHash,
		}
	}
	payload := struct {
		System string         `json:"system"`
		Config explore.Config `json:"config"`
		Seeds  []fpSeed       `json:"seeds"`
	}{
		System: m.System,
		Config: m.Config,
		Seeds:  seeds,
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Verdict returns the aggregate verdict of the campaign.
func (m *Manifest) Verdict() checker.Verdict {
	if m.Violations > 0 {
		return checker.Violation
	}
	if m.Unknown > 0 || m.Errors > 0 {
		return checker.Unknown
	}
	return checker.Linearizable
}

// LoadManifest reads a campaign Manifest from a JSON file on disk.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read campaign manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse campaign manifest %s: %w", path, err)
	}
	sort.Slice(m.Seeds, func(i, j int) bool { return m.Seeds[i].Seed < m.Seeds[j].Seed })
	return &m, nil
}

// WriteManifest writes the campaign Manifest as indented JSON to path.
func WriteManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
