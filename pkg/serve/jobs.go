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
	Tenant      string          `json:"tenant,omitempty"`
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
	Tenant   string `json:"tenant,omitempty"`
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
	tenant := strings.TrimSpace(r.URL.Query().Get("tenant"))
	points := s.store.Trends(target, tenant)
	resp := map[string]any{
		"target": target,
		"total":  len(points),
		"points": points,
	}
	if tenant != "" {
		resp["tenant"] = normalizeTenant(tenant)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
		tenantFilter := strings.TrimSpace(r.URL.Query().Get("tenant"))
		if tenantFilter != "" {
			tenantFilter = normalizeTenant(tenantFilter)
		}
		authed := s.hasValidAuth(r)
		s.jobMu.Lock()
		var out []Job
		for i := len(s.jobs) - 1; i >= 0; i-- {
			j := *s.jobs[i]
			if statusFilter != "" && j.Status != statusFilter {
				continue
			}
			if tenantFilter != "" && normalizeTenant(j.Tenant) != tenantFilter {
				continue
			}
			if !authed {
				j.LiveConfig = nil
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
		const maxJobBodyBytes = 4 * 1024 * 1024
		body, err := io.ReadAll(io.LimitReader(r.Body, maxJobBodyBytes+1))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if len(body) > maxJobBodyBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "job request body exceeds 4 MiB limit"})
			return
		}
		var req Job
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid json: %v", err)})
			return
		}
		if req.Tenant == "" {
			req.Tenant = strings.TrimSpace(r.URL.Query().Get("tenant"))
		}
		if req.Tenant != "" {
			req.Tenant = normalizeTenant(req.Tenant)
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
			Tenant:     req.Tenant,
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
	authed := s.hasValidAuth(r)
	s.jobMu.Lock()
	var found *Job
	for _, j := range s.jobs {
		if j.ID == sub {
			c := *j
			if !authed {
				c.LiveConfig = nil
			}
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
	if req.Tenant == "" {
		req.Tenant = strings.TrimSpace(r.URL.Query().Get("tenant"))
	}
	wantTenant := ""
	if req.Tenant != "" {
		wantTenant = normalizeTenant(req.Tenant)
	}

	s.jobMu.Lock()
	var claimed *Job
	for _, j := range s.jobs {
		if j.Status != JobPending {
			continue
		}
		if wantTenant != "" && normalizeTenant(j.Tenant) != wantTenant {
			continue
		}
		j.Status = JobClaimed
		j.WorkerID = req.WorkerID
		j.ClaimedAt = time.Now().UTC().Format(time.RFC3339)
		_ = s.saveJobsLocked()
		c := *j
		claimed = &c
		break
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
	const maxCompleteBodyBytes = 32 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCompleteBodyBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(body) > maxCompleteBodyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "job completion body exceeds 32 MiB limit"})
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
	if targetJob.Status == JobCompleted || targetJob.Status == JobFailed {
		s.jobMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("job %q is already %s", id, targetJob.Status)})
		return
	}
	if targetJob.WorkerID != "" && req.WorkerID != "" && targetJob.WorkerID != req.WorkerID {
		s.jobMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("job %q is claimed by worker %q, not %q", id, targetJob.WorkerID, req.WorkerID)})
		return
	}
	tenant := targetJob.Tenant
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
		res, err := s.store.IngestCampaign(req.Campaign, tenant)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		res.Webhooks = s.notifyWebhooks(res, nil)
		ingestRes = res
	} else if req.Report != nil {
		priv, signer := s.signingKeyForTenant(tenant)
		res, err := s.store.IngestReport(req.Report, commit, branch, priv, signer, tenant)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		res.Webhooks = s.notifyWebhooks(res, req.Report)
		ingestRes = res
	}

	s.jobMu.Lock()
	if targetJob.Status == JobCompleted || targetJob.Status == JobFailed {
		s.jobMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("job %q is already %s", id, targetJob.Status)})
		return
	}
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
