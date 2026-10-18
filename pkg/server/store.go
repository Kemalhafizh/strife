package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/campaign"
	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// Run is what the registry knows about one pushed run (meta.json).
type Run struct {
	ID      string          `json:"id"` // the report or campaign fingerprint
	Project string          `json:"project"`
	Kind    string          `json:"kind"` // simulation, live, check, campaign
	Target  string          `json:"target"`
	Series  string          `json:"series"` // runs compared with each other
	Seed    uint64          `json:"seed,omitempty"`
	Verdict checker.Verdict `json:"verdict"`
	Ops     int             `json:"ops"`
	Faults  []nemesis.Kind  `json:"faults,omitempty"`

	// Campaigns only.
	Seeds      int `json:"seeds,omitempty"`
	Violations int `json:"violations,omitempty"`

	Received time.Time `json:"received"`
	Commit   string    `json:"commit,omitempty"`
	Branch   string    `json:"branch,omitempty"`
	Source   string    `json:"source,omitempty"` // e.g. the CI run URL

	HasHistory      bool   `json:"has_history"`
	HistoryVerified bool   `json:"history_verified"` // its hash matches the report
	Attested        bool   `json:"attested"`         // signed by a trusted key
	Signer          string `json:"signer,omitempty"`
	KeyID           string `json:"key_id,omitempty"`

	// Change compares the run with the previous run of its series:
	// "regression", "fixed", "new" (first of its series) or "" (same).
	Change   string               `json:"change,omitempty"`
	Previous string               `json:"previous,omitempty"`
	Diff     *campaign.Comparison `json:"diff,omitempty"`
}

// Changes of a run relative to the previous one in its series.
const (
	ChangeRegression = "regression"
	ChangeFixed      = "fixed"
	ChangeNew        = "new"
)

var validID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Artifact files a run directory may hold.
const (
	fileMeta     = "meta.json"
	fileReport   = "report.json"
	fileCampaign = "campaign.json"
	fileHistory  = "history.jsonl"
	fileBundle   = "bundle.json"
)

// store keeps runs on disk under <data>/projects/<project>/runs/<id>/ and
// an index in memory, rebuilt from the meta files at start.
type store struct {
	dir string
	mu  sync.RWMutex
	// runs per project, oldest first.
	runs map[string][]*Run
}

func openStore(dir string, projects map[string]*project) (*store, error) {
	s := &store{dir: dir, runs: map[string][]*Run{}}
	for name := range projects {
		pdir := filepath.Join(dir, "projects", name, "runs")
		if err := os.MkdirAll(pdir, 0o750); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(pdir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() || !validID.MatchString(e.Name()) {
				continue // includes half-written ".tmp-" directories
			}
			data, err := os.ReadFile(filepath.Join(pdir, e.Name(), fileMeta))
			if err != nil {
				continue
			}
			var r Run
			if json.Unmarshal(data, &r) == nil && r.ID == e.Name() {
				s.runs[name] = append(s.runs[name], &r)
			}
		}
		sort.SliceStable(s.runs[name], func(i, j int) bool { return s.runs[name][i].Received.Before(s.runs[name][j].Received) })
	}
	return s, nil
}

func (s *store) runDir(project, id string) string {
	return filepath.Join(s.dir, "projects", project, "runs", id)
}

// get returns a run by ID.
func (s *store) get(project, id string) *Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.runs[project] {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// list returns the runs matching filter, newest first.
func (s *store) list(project string, keep func(*Run) bool, limit int) []*Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := s.runs[project]
	var out []*Run
	for i := len(all) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		if keep == nil || keep(all[i]) {
			out = append(out, all[i])
		}
	}
	return out
}

// latest returns the newest run of a series.
func (s *store) latest(project, series string) *Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := s.runs[project]
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Series == series {
			return all[i]
		}
	}
	return nil
}

var errExists = errors.New("run already stored")

// put stores a run and its files atomically: they are written to a
// temporary directory that is renamed into place, so a crash never leaves
// a half-stored run.
func (s *store) put(r *Run, files map[string][]byte, retain int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.runs[r.Project] {
		if x.ID == r.ID {
			return errExists
		}
	}
	final := s.runDir(r.Project, r.ID)
	tmp, err := os.MkdirTemp(filepath.Dir(final), ".tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	meta, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	files[fileMeta] = append(meta, '\n')
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(tmp, name), data, 0o640); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("store run %s: %w", r.ID, err)
	}
	s.runs[r.Project] = append(s.runs[r.Project], r)
	if retain > 0 {
		for len(s.runs[r.Project]) > retain {
			old := s.runs[r.Project][0]
			s.runs[r.Project] = s.runs[r.Project][1:]
			_ = os.RemoveAll(s.runDir(r.Project, old.ID))
		}
	}
	return nil
}

// file reads one artifact of a run.
func (s *store) file(project, id, name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.runDir(project, id), name))
}
