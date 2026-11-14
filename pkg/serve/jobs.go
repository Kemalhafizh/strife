package serve

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/campaign"
	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/report"
)

// Job status values.
const (
	JobPending   = "pending"
	JobClaimed   = "claimed"
	JobCompleted = "completed"
	JobFailed    = "failed"
)

// Job represents a verification task queued in strife serve for pull-based
// execution by a remote or private-VPC strife worker.
type Job struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"` // "sim", "campaign", or "run"
	System      string          `json:"system,omitempty"`
	Seed        uint64          `json:"seed,omitempty"`
	Seeds       string          `json:"seeds,omitempty"`
	Nodes       int             `json:"nodes,omitempty"`
	Clients     int             `json:"clients,omitempty"`
	Duration    string          `json:"duration,omitempty"`
	Faults      string          `json:"faults,omitempty"`
	LiveConfig  json.RawMessage `json:"live_config,omitempty"`
	Commit      string          `json:"commit,omitempty"`
	Branch      string          `json:"branch,omitempty"`
	Status      string          `json:"status"`
	WorkerID    string          `json:"worker_id,omitempty"`
	CreatedAt   string          `json:"created_at"`
	ClaimedAt   string          `json:"claimed_at,omitempty"`
	CompletedAt string          `json:"completed_at,omitempty"`
	RunID       string          `json:"run_id,omitempty"`
	Verdict     checker.Verdict `json:"verdict,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// JobClaimRequest is sent by a worker to claim the next pending job.
type JobClaimRequest struct {
	WorkerID string `json:"worker_id"`
}

// JobCompleteRequest is sent by a worker when a claimed job finishes.
type JobCompleteRequest struct {
	WorkerID string             `json:"worker_id,omitempty"`
	Error    string             `json:"error,omitempty"`
	Report   *report.Report     `json:"report,omitempty"`
	Campaign *campaign.Manifest `json:"campaign,omitempty"`
}

func (s *Server) loadJobs() error {
	p := filepath.Join(s.store.Dir(), "jobs.json")
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []*Job
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	s.jobs = list
	s.jobSeq = uint64(len(list))
	return nil
}

func (s *Server) saveJobsLocked() error {
	p := filepath.Join(s.store.Dir(), "jobs.json")
	data, err := json.MarshalIndent(s.jobs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o644)
}

func (s *Server) handleTrends(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	points := s.store.Trends(target)
	writeJSON(w, http.StatusOK, map[string]any{
		"target": target,
		"total":  len(points),
		"points": points,
	})
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
		s.jobMu.Lock()
		var out []Job
		for i := len(s.jobs) - 1; i >= 0; i-- {
			j := *s.jobs[i]
			if statusFilter != "" && j.Status != statusFilter {
				continue
			}
			out = append(out, j)
		}
		s.jobMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"total": len(out),
			"jobs":  out,
		})

	case http.MethodPost:
		if !s.checkAuth(w, r) {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		var req Job
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid json: %v", err)})
			return
		}
		if req.Kind == "" {
			req.Kind = "sim"
		}
		switch req.Kind {
		case "sim", "campaign":
			if req.System == "" {
				req.System = "raft"
			}
		case "run":
			if len(req.LiveConfig) == 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "live_config is required for kind=run"})
				return
			}
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unsupported job kind %q (sim, campaign, run)", req.Kind)})
			return
		}

		s.jobMu.Lock()
		s.jobSeq++
		job := &Job{
			ID:         fmt.Sprintf("job-%04d", s.jobSeq),
			Kind:       req.Kind,
			System:     req.System,
			Seed:       req.Seed,
			Seeds:      req.Seeds,
			Nodes:      req.Nodes,
			Clients:    req.Clients,
			Duration:   req.Duration,
			Faults:     req.Faults,
			LiveConfig: req.LiveConfig,
			Commit:     req.Commit,
			Branch:     req.Branch,
			Status:     JobPending,
			CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		}
		if job.Kind == "sim" && job.Seed == 0 {
			job.Seed = 1
		}
		if job.Kind == "campaign" && job.Seeds == "" {
			job.Seeds = "1..20"
		}
		s.jobs = append(s.jobs, job)
		_ = s.saveJobsLocked()
		copied := *job
		s.jobMu.Unlock()
		writeJSON(w, http.StatusCreated, copied)

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) handleJobsSubpath(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimPrefix(r.URL.Path, "/api/v1/jobs/")
	sub = strings.TrimSpace(strings.TrimSuffix(sub, "/"))
	if sub == "" {
		s.handleJobs(w, r)
		return
	}
	if sub == "claim" {
		s.handleJobClaim(w, r)
		return
	}
	if strings.HasSuffix(sub, "/complete") {
		id := strings.TrimSuffix(sub, "/complete")
		s.handleJobComplete(w, r, id)
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s.jobMu.Lock()
	var found *Job
	for _, j := range s.jobs {
		if j.ID == sub {
			c := *j
			found = &c
			break
		}
	}
	s.jobMu.Unlock()
	if found == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("job %q not found", sub)})
		return
	}
	writeJSON(w, http.StatusOK, found)
}

func (s *Server) handleJobClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.checkAuth(w, r) {
		return
	}
	var req JobClaimRequest
	_ = json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req)
	if req.WorkerID == "" {
		req.WorkerID = strings.TrimSpace(r.URL.Query().Get("worker_id"))
	}
	if req.WorkerID == "" {
		req.WorkerID = "strife-worker"
	}

	s.jobMu.Lock()
	var claimed *Job
	for _, j := range s.jobs {
		if j.Status == JobPending {
			j.Status = JobClaimed
			j.WorkerID = req.WorkerID
			j.ClaimedAt = time.Now().UTC().Format(time.RFC3339)
			_ = s.saveJobsLocked()
			c := *j
			claimed = &c
			break
		}
	}
	s.jobMu.Unlock()

	if claimed == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, claimed)
}

func (s *Server) handleJobComplete(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.checkAuth(w, r) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32*1024*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var req JobCompleteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid json: %v", err)})
		return
	}

	s.jobMu.Lock()
	var targetJob *Job
	for _, j := range s.jobs {
		if j.ID == id {
			targetJob = j
			break
		}
	}
	if targetJob == nil {
		s.jobMu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("job %q not found", id)})
		return
	}
	commit := targetJob.Commit
	branch := targetJob.Branch
	s.jobMu.Unlock()

	var ingestRes *IngestResult
	if req.Campaign != nil {
		if commit != "" && req.Campaign.Commit == "" {
			req.Campaign.Commit = commit
		}
		if branch != "" && req.Campaign.Branch == "" {
			req.Campaign.Branch = branch
		}
		res, err := s.store.IngestCampaign(req.Campaign)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		res.Webhooks = s.notifyWebhooks(res, nil)
		ingestRes = res
	} else if req.Report != nil {
		res, err := s.store.IngestReport(req.Report, commit, branch, s.privKey, s.cfg.Signer)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		res.Webhooks = s.notifyWebhooks(res, req.Report)
		ingestRes = res
	}

	s.jobMu.Lock()
	targetJob.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	if req.WorkerID != "" {
		targetJob.WorkerID = req.WorkerID
	}
	targetJob.Error = req.Error
	if ingestRes != nil {
		targetJob.RunID = ingestRes.Entry.ID
		targetJob.Verdict = ingestRes.Entry.Verdict
		targetJob.Status = JobCompleted
	} else if req.Error != "" {
		targetJob.Status = JobFailed
	} else {
		targetJob.Status = JobCompleted
	}
	_ = s.saveJobsLocked()
	jobCopy := *targetJob
	s.jobMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"job":    jobCopy,
		"ingest": ingestRes,
	})
}
