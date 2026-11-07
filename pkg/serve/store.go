// Package serve implements Strife's self-hosted continuous verification and
// run registry daemon (`strife serve`). It provides an append-only artifact
// store, automatic seed-level regression comparison across ingested campaigns,
// optional Ed25519 in-toto signing, a REST API, and an embedded web UI.
package serve

import (
	"bufio"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/attest"
	"github.com/kemalhafizh/strife/pkg/campaign"
	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/report"
)

// RunEntry is one indexed record in the persistent registry.jsonl log.
type RunEntry struct {
	ID                      string          `json:"id"`
	Kind                    string          `json:"kind"` // "report" or "campaign"
	RunKind                 string          `json:"run_kind,omitempty"`
	Target                  string          `json:"target"`
	Model                   string          `json:"model"`
	Seed                    uint64          `json:"seed,omitempty"`
	TotalSeeds              int             `json:"total_seeds"`
	Passed                  int             `json:"passed"`
	Violations              int             `json:"violations"`
	Operations              int             `json:"operations"`
	Verdict                 checker.Verdict `json:"verdict"`
	Fingerprint             string          `json:"fingerprint"`
	Commit                  string          `json:"commit,omitempty"`
	Branch                  string          `json:"branch,omitempty"`
	Timestamp               string          `json:"timestamp"`
	Signed                  bool            `json:"signed"`
	Signer                  string          `json:"signer,omitempty"`
	KeyID                   string          `json:"key_id,omitempty"`
	Regressions             int             `json:"regressions"`
	Fixes                   int             `json:"fixes"`
	TraceDrifts             int             `json:"trace_drifts"`
	OverallAvailability     float64         `json:"overall_availability,omitempty"`
	FaultWindowAvailability float64         `json:"fault_window_availability,omitempty"`
	MTTRP50Ms               int64           `json:"mttr_p50_ms,omitempty"`
	MTTRP95Ms               int64           `json:"mttr_p95_ms,omitempty"`
	MTTRMaxMs               int64           `json:"mttr_max_ms,omitempty"`
}

// IngestResult is returned after a report or campaign is ingested into the Store.
type IngestResult struct {
	Entry      RunEntry             `json:"entry"`
	Comparison *campaign.Comparison `json:"comparison,omitempty"`
	Bundle     *attest.Bundle       `json:"bundle,omitempty"`
	Webhooks   []WebhookDelivery    `json:"webhooks,omitempty"`
}

// Store manages the persistent run registry directory.
type Store struct {
	mu        sync.RWMutex
	dir       string
	entries   []RunEntry
	byID      map[string]RunEntry
	baselines map[string]*campaign.Manifest // system -> latest campaign manifest
	seq       uint64
}

// NewStore opens or initializes a persistent Store at dir.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		dir = "strife-registry"
	}
	runsDir := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return nil, fmt.Errorf("serve: create store dir %s: %w", dir, err)
	}
	s := &Store{
		dir:       dir,
		byID:      make(map[string]RunEntry),
		baselines: make(map[string]*campaign.Manifest),
	}
	if err := s.loadExisting(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) loadExisting() error {
	regPath := filepath.Join(s.dir, "registry.jsonl")
	f, err := os.Open(regPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("serve: open registry %s: %w", regPath, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e RunEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		s.entries = append(s.entries, e)
		s.byID[e.ID] = e
		s.seq++
		if e.Kind == "campaign" {
			cPath := filepath.Join(s.dir, "runs", e.ID, "campaign.json")
			if m, err := campaign.LoadManifest(cPath); err == nil {
				s.baselines[m.System] = m
			}
		}
	}
	return sc.Err()
}

// Dir returns the root directory of the store.
func (s *Store) Dir() string {
	return s.dir
}

// List returns stored RunEntry items ordered newest-first, optionally filtered
// by target or kind.
func (s *Store) List(target, kind string) []RunEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []RunEntry
	for i := len(s.entries) - 1; i >= 0; i-- {
		e := s.entries[i]
		if target != "" && e.Target != target {
			continue
		}
		if kind != "" && e.Kind != kind {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Lookup finds a RunEntry and its raw JSON artifact payload by ID.
func (s *Store) Lookup(id string) (RunEntry, map[string]any, error) {
	s.mu.RLock()
	e, ok := s.byID[id]
	s.mu.RUnlock()
	if !ok {
		return RunEntry{}, nil, fmt.Errorf("run %q not found", id)
	}

	runDir := filepath.Join(s.dir, "runs", id)
	out := map[string]any{
		"entry": e,
	}
	if e.Kind == "campaign" {
		if data, err := os.ReadFile(filepath.Join(runDir, "campaign.json")); err == nil {
			var raw json.RawMessage = data
			out["campaign"] = raw
		}
	} else {
		if data, err := os.ReadFile(filepath.Join(runDir, "report.json")); err == nil {
			var raw json.RawMessage = data
			out["report"] = raw
		}
	}
	if data, err := os.ReadFile(filepath.Join(runDir, "bundle.json")); err == nil {
		var raw json.RawMessage = data
		out["bundle"] = raw
	}
	return e, out, nil
}

// IngestReport validates, optionally signs, and persists a single-seed Report.
func (s *Store) IngestReport(rep *report.Report, commit, branch string, priv ed25519.PrivateKey, signer string) (*IngestResult, error) {
	if rep == nil {
		return nil, fmt.Errorf("serve: report is nil")
	}
	recomputed := report.Fingerprint(rep)
	if rep.Fingerprint != "" && rep.Fingerprint != recomputed {
		return nil, fmt.Errorf("serve: report fingerprint mismatch: recorded %s vs recomputed %s", rep.Fingerprint, recomputed)
	}
	rep.Fingerprint = recomputed

	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	now := time.Now().UTC()
	id := fmt.Sprintf("run-%04d-%s", s.seq, shortFP(rep.Fingerprint))
	runDir := filepath.Join(s.dir, "runs", id)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}

	modelName := rep.Check.Model
	if modelName == "" {
		modelName = "kv"
	}
	passed, violations := 0, 0
	if rep.Verdict == checker.Linearizable {
		passed = 1
	} else if rep.Verdict == checker.Violation {
		violations = 1
	}

	entry := RunEntry{
		ID:          id,
		Kind:        "report",
		RunKind:     rep.Kind,
		Target:      rep.Target,
		Model:       modelName,
		Seed:        rep.Seed,
		TotalSeeds:  1,
		Passed:      passed,
		Violations:  violations,
		Operations:  rep.Check.Operations,
		Verdict:     rep.Verdict,
		Fingerprint: rep.Fingerprint,
		Commit:      commit,
		Branch:      branch,
		Timestamp:   now.Format(time.RFC3339),
	}
	if rep.Resilience != nil {
		entry.OverallAvailability = rep.Resilience.OverallAvailability
		entry.FaultWindowAvailability = rep.Resilience.FaultWindowAvailability
		entry.MTTRP50Ms = rep.Resilience.MTTR.P50.Milliseconds()
		entry.MTTRP95Ms = rep.Resilience.MTTR.P95.Milliseconds()
		entry.MTTRMaxMs = rep.Resilience.MTTR.Max.Milliseconds()
	}

	var bundle *attest.Bundle
	if len(priv) == ed25519.PrivateKeySize {
		if signer == "" {
			signer = "strife-serve"
		}
		b, err := attest.Sign(rep, priv, signer)
		if err != nil {
			return nil, err
		}
		bundle = b
		entry.Signed = true
		entry.Signer = signer
		if len(b.Signatures) > 0 {
			entry.KeyID = b.Signatures[0].KeyID
		}
		bBytes, _ := json.MarshalIndent(b, "", "  ")
		_ = os.WriteFile(filepath.Join(runDir, "bundle.json"), append(bBytes, '\n'), 0o644)
	}

	rBytes, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(runDir, "report.json"), append(rBytes, '\n'), 0o644); err != nil {
		return nil, err
	}

	if err := s.appendEntry(entry); err != nil {
		return nil, err
	}
	return &IngestResult{Entry: entry, Bundle: bundle}, nil
}

// IngestCampaign validates, compares against the latest baseline for the same
// target system, and persists a multi-seed campaign Manifest.
func (s *Store) IngestCampaign(m *campaign.Manifest) (*IngestResult, error) {
	if m == nil {
		return nil, fmt.Errorf("serve: campaign manifest is nil")
	}
	recomputed := m.ComputeFingerprint()
	if m.Fingerprint != "" && m.Fingerprint != recomputed {
		return nil, fmt.Errorf("serve: campaign fingerprint mismatch: recorded %s vs recomputed %s", m.Fingerprint, recomputed)
	}
	m.Fingerprint = recomputed

	s.mu.Lock()
	defer s.mu.Unlock()

	var comp *campaign.Comparison
	if prev, ok := s.baselines[m.System]; ok {
		comp = campaign.Compare(prev, m)
		m.Comparison = comp
	} else if m.Comparison != nil {
		comp = m.Comparison
	}
	s.baselines[m.System] = m

	s.seq++
	now := time.Now().UTC()
	id := fmt.Sprintf("camp-%04d-%s", s.seq, shortFP(m.Fingerprint))
	runDir := filepath.Join(s.dir, "runs", id)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}

	modelName := "kv"
	if strings.Contains(m.System, "ledger") {
		modelName = "ledger"
	} else if strings.Contains(m.System, "outbox") || strings.Contains(m.System, "settlement") {
		modelName = "settlement"
	}

	ts := m.Timestamp
	if ts == "" {
		ts = now.Format(time.RFC3339)
	}

	entry := RunEntry{
		ID:          id,
		Kind:        "campaign",
		RunKind:     "campaign",
		Target:      m.System,
		Model:       modelName,
		TotalSeeds:  m.TotalSeeds,
		Passed:      m.Passed,
		Violations:  m.Violations,
		Operations:  m.TotalOps,
		Verdict:     m.Verdict(),
		Fingerprint: m.Fingerprint,
		Commit:      m.Commit,
		Branch:      m.Branch,
		Timestamp:   ts,
	}
	if comp != nil {
		entry.Regressions = len(comp.Regressed)
		entry.Fixes = len(comp.Fixed)
		entry.TraceDrifts = len(comp.TraceDrifts)
	}

	if err := campaign.WriteManifest(filepath.Join(runDir, "campaign.json"), m); err != nil {
		return nil, err
	}
	if err := s.appendEntry(entry); err != nil {
		return nil, err
	}
	return &IngestResult{Entry: entry, Comparison: comp}, nil
}

func (s *Store) appendEntry(e RunEntry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	regPath := filepath.Join(s.dir, "registry.jsonl")
	f, err := os.OpenFile(regPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	s.entries = append(s.entries, e)
	s.byID[e.ID] = e
	return nil
}

// Targets returns the sorted list of unique system/cluster targets in the store.
func (s *Store) Targets() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, e := range s.entries {
		if e.Target != "" && !slices.Contains(out, e.Target) {
			out = append(out, e.Target)
		}
	}
	slices.Sort(out)
	return out
}

// LoadDossierInputs loads the stored report or campaign manifest and optional
// attestation bundle for run ID id.
func (s *Store) LoadDossierInputs(id string) (*report.Report, *campaign.Manifest, *attest.Bundle, error) {
	s.mu.RLock()
	e, ok := s.byID[id]
	s.mu.RUnlock()
	if !ok {
		return nil, nil, nil, fmt.Errorf("run %q not found", id)
	}
	runDir := filepath.Join(s.dir, "runs", id)
	var (
		rep    *report.Report
		camp   *campaign.Manifest
		bundle *attest.Bundle
	)
	if e.Kind == "campaign" {
		m, err := campaign.LoadManifest(filepath.Join(runDir, "campaign.json"))
		if err != nil {
			return nil, nil, nil, err
		}
		camp = m
	} else {
		data, err := os.ReadFile(filepath.Join(runDir, "report.json"))
		if err != nil {
			return nil, nil, nil, err
		}
		var r report.Report
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, nil, nil, err
		}
		rep = &r
	}
	if bData, err := os.ReadFile(filepath.Join(runDir, "bundle.json")); err == nil {
		var b attest.Bundle
		if err := json.Unmarshal(bData, &b); err == nil {
			bundle = &b
		}
	}
	return rep, camp, bundle, nil
}

func shortFP(h string) string {
	if len(h) >= 8 {
		return h[:8]
	}
	if h == "" {
		return "00000000"
	}
	return h
}
