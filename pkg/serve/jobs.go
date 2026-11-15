package serve

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	ID              string          `json:"id"`
	Tenant          string          `json:"tenant,omitempty"`
	Kind            string          `json:"kind"` // "sim", "campaign", or "run"
	System          string          `json:"system,omitempty"`
	Seed            uint64          `json:"seed,omitempty"`
	Seeds           string          `json:"seeds,omitempty"`
	Nodes           int             `json:"nodes,omitempty"`
	Clients         int             `json:"clients,omitempty"`
	Duration        string          `json:"duration,omitempty"`
	Faults          string          `json:"faults,omitempty"`
	LiveConfig      json.RawMessage `json:"live_config,omitempty"`
	Commit          string          `json:"commit,omitempty"`
	Branch          string          `json:"branch,omitempty"`
	Status          string          `json:"status"`
	WorkerID        string          `json:"worker_id,omitempty"`
	ClaimToken      string          `json:"claim_token,omitempty"`
	CreatedAt       string          `json:"created_at"`
	ClaimedAt       string          `json:"claimed_at,omitempty"`
	LastHeartbeatAt string          `json:"last_heartbeat_at,omitempty"`
	LeaseExpiresAt  string          `json:"lease_expires_at,omitempty"`
	CompletedAt     string          `json:"completed_at,omitempty"`
	Attempts        int             `json:"attempts,omitempty"`
	RunID           string          `json:"run_id,omitempty"`
	Verdict         checker.Verdict `json:"verdict,omitempty"`
	Error           string          `json:"error,omitempty"`

	completing bool `json:"-"`
}

// JobClaimRequest is sent by a worker to claim the next pending job.
type JobClaimRequest struct {
	WorkerID string `json:"worker_id"`
	Tenant   string `json:"tenant,omitempty"`
}

// JobHeartbeatRequest is sent periodically by a claiming worker to renew its lease.
type JobHeartbeatRequest struct {
	WorkerID   string `json:"worker_id"`
	ClaimToken string `json:"claim_token"`
}

// JobCompleteRequest is sent by a worker when a claimed job finishes.
type JobCompleteRequest struct {
	WorkerID   string             `json:"worker_id,omitempty"`
	ClaimToken string             `json:"claim_token,omitempty"`
	Error      string             `json:"error,omitempty"`
	Report     *report.Report     `json:"report,omitempty"`
	Campaign   *campaign.Manifest `json:"campaign,omitempty"`
}

func newClaimToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func claimTokensEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) isJobLeaseExpired(j *Job, now time.Time) bool {
	if j.Status != JobClaimed || j.completing {
		return false
	}
	if j.LeaseExpiresAt != "" {
		exp, err := time.Parse(time.RFC3339Nano, j.LeaseExpiresAt)
		if err != nil {
			return true
		}
		return !now.Before(exp)
	}
	if j.ClaimedAt != "" {
		claimed, err := time.Parse(time.RFC3339Nano, j.ClaimedAt)
		if err == nil {
			return !now.Before(claimed.Add(s.cfg.JobLease))
		}
	}
	return true
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
	maxSeq := uint64(len(list))
	for _, j := range list {
		if j == nil {
			continue
		}
		if rest, ok := strings.CutPrefix(j.ID, "job-"); ok {
			if n, perr := strconv.ParseUint(rest, 10, 64); perr == nil && n > maxSeq {
				maxSeq = n
			}
		}
	}
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	s.jobs = list
	s.jobSeq = maxSeq
	return nil
}

func (s *Server) saveJobsLocked() error {
	p := filepath.Join(s.store.Dir(), "jobs.json")
	data, err := json.MarshalIndent(s.jobs, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFileSync(p, append(data, '\n'), 0o600)
}

func (s *Server) handleTrends(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	idAuth, ok := s.checkAuthIdentity(w, r)
	if !ok {
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	tenant := strings.TrimSpace(r.URL.Query().Get("tenant"))
	if !idAuth.isAdmin {
		if tenant != "" && normalizeTenant(tenant) != idAuth.tenant {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: tenant mismatch"})
			return
		}
		tenant = idAuth.tenant
	}
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
		idAuth, ok := s.checkAuthIdentity(w, r)
		if !ok {
			return
		}
		statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
		tenantFilter := strings.TrimSpace(r.URL.Query().Get("tenant"))
		if tenantFilter != "" {
			tenantFilter = normalizeTenant(tenantFilter)
		}
		if !idAuth.isAdmin {
			if tenantFilter != "" && tenantFilter != idAuth.tenant {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: tenant mismatch"})
				return
			}
			tenantFilter = idAuth.tenant
		}
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
			j.ClaimToken = ""
			out = append(out, j)
		}
		s.jobMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"total": len(out),
			"jobs":  out,
		})

	case http.MethodPost:
		idAuth, ok := s.checkAuthIdentity(w, r)
		if !ok {
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
		if !idAuth.isAdmin {
			if req.Tenant != "" && req.Tenant != idAuth.tenant {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: tenant mismatch"})
				return
			}
			req.Tenant = idAuth.tenant
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
		if err := s.saveJobsLocked(); err != nil {
			s.jobs = s.jobs[:len(s.jobs)-1]
			s.jobSeq--
			s.jobMu.Unlock()
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("persist job: %v", err)})
			return
		}
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
	if strings.HasSuffix(sub, "/heartbeat") {
		id := strings.TrimSuffix(sub, "/heartbeat")
		s.handleJobHeartbeat(w, r, id)
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
	idAuth, ok := s.checkAuthIdentity(w, r)
	if !ok {
		return
	}
	s.jobMu.Lock()
	var found *Job
	for _, j := range s.jobs {
		if j.ID == sub {
			c := *j
			c.ClaimToken = ""
			found = &c
			break
		}
	}
	s.jobMu.Unlock()
	if found == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("job %q not found", sub)})
		return
	}
	if !idAuth.allowsTenant(found.Tenant) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: job belongs to another tenant"})
		return
	}
	writeJSON(w, http.StatusOK, found)
}

func (s *Server) handleJobClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	idAuth, ok := s.checkAuthIdentity(w, r)
	if !ok {
		return
	}
	const maxClaimBodyBytes = 64 * 1024
	body, err := io.ReadAll(io.LimitReader(r.Body, maxClaimBodyBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(body) > maxClaimBodyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "job claim body exceeds 64 KiB limit"})
		return
	}
	var req JobClaimRequest
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid json: %v", err)})
			return
		}
	}
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
	if !idAuth.isAdmin {
		if wantTenant != "" && wantTenant != idAuth.tenant {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: tenant mismatch"})
			return
		}
		wantTenant = idAuth.tenant
	}

	s.jobMu.Lock()
	var claimed *Job
	var saveErr error
	now := time.Now().UTC()
	for _, j := range s.jobs {
		if j.Status != JobPending && !s.isJobLeaseExpired(j, now) {
			continue
		}
		if wantTenant != "" && normalizeTenant(j.Tenant) != wantTenant {
			continue
		}
		tok, tokErr := newClaimToken()
		if tokErr != nil {
			saveErr = tokErr
			break
		}
		prev := *j
		nowStr := now.Format(time.RFC3339Nano)
		j.Status = JobClaimed
		j.WorkerID = req.WorkerID
		j.ClaimToken = tok
		j.ClaimedAt = nowStr
		j.LastHeartbeatAt = nowStr
		j.LeaseExpiresAt = now.Add(s.cfg.JobLease).Format(time.RFC3339Nano)
		j.Attempts++
		if err := s.saveJobsLocked(); err != nil {
			*j = prev
			saveErr = err
			break
		}
		c := *j
		claimed = &c
		break
	}
	s.jobMu.Unlock()

	if saveErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("persist job claim: %v", saveErr)})
		return
	}
	if claimed == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, claimed)
}

func (s *Server) handleJobHeartbeat(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	idAuth, ok := s.checkAuthIdentity(w, r)
	if !ok {
		return
	}
	const maxHeartbeatBodyBytes = 64 * 1024
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHeartbeatBodyBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(body) > maxHeartbeatBodyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "job heartbeat body exceeds 64 KiB limit"})
		return
	}
	var req JobHeartbeatRequest
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
	if !idAuth.allowsTenant(targetJob.Tenant) {
		s.jobMu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: job belongs to another tenant"})
		return
	}
	if targetJob.Status != JobClaimed || targetJob.completing {
		status := targetJob.Status
		s.jobMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("job %q cannot accept heartbeat in state %q", id, status)})
		return
	}
	now := time.Now().UTC()
	if s.isJobLeaseExpired(targetJob, now) {
		s.jobMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("job %q claim lease has expired", id)})
		return
	}
	if req.WorkerID == "" || req.WorkerID != targetJob.WorkerID {
		claimedBy := targetJob.WorkerID
		s.jobMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("job %q is claimed by worker %q, not %q", id, claimedBy, req.WorkerID)})
		return
	}
	if targetJob.ClaimToken != "" && !claimTokensEqual(req.ClaimToken, targetJob.ClaimToken) {
		s.jobMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("job %q claim token mismatch", id)})
		return
	}
	prev := *targetJob
	targetJob.LastHeartbeatAt = now.Format(time.RFC3339Nano)
	targetJob.LeaseExpiresAt = now.Add(s.cfg.JobLease).Format(time.RFC3339Nano)
	if err := s.saveJobsLocked(); err != nil {
		*targetJob = prev
		s.jobMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("persist job heartbeat: %v", err)})
		return
	}
	jobCopy := *targetJob
	jobCopy.ClaimToken = ""
	s.jobMu.Unlock()
	writeJSON(w, http.StatusOK, jobCopy)
}

func (s *Server) handleJobComplete(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	idAuth, ok := s.checkAuthIdentity(w, r)
	if !ok {
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
	if !idAuth.allowsTenant(targetJob.Tenant) {
		s.jobMu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: job belongs to another tenant"})
		return
	}
	if targetJob.Status != JobClaimed {
		status := targetJob.Status
		s.jobMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("job %q is %s, not claimed", id, status)})
		return
	}
	if targetJob.completing {
		s.jobMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("job %q completion already in progress", id)})
		return
	}
	if req.WorkerID == "" || (targetJob.WorkerID != "" && targetJob.WorkerID != req.WorkerID) {
		claimedBy := targetJob.WorkerID
		s.jobMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("job %q is claimed by worker %q, not %q", id, claimedBy, req.WorkerID)})
		return
	}
	if targetJob.ClaimToken != "" && !claimTokensEqual(req.ClaimToken, targetJob.ClaimToken) {
		s.jobMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("job %q claim token mismatch", id)})
		return
	}
	targetJob.completing = true
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
			s.jobMu.Lock()
			targetJob.completing = false
			s.jobMu.Unlock()
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		res.Webhooks = s.notifyWebhooks(res, nil)
		ingestRes = res
	} else if req.Report != nil {
		priv, signer := s.signingKeyForTenant(tenant)
		res, err := s.store.IngestReport(req.Report, commit, branch, priv, signer, tenant)
		if err != nil {
			s.jobMu.Lock()
			targetJob.completing = false
			s.jobMu.Unlock()
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		res.Webhooks = s.notifyWebhooks(res, req.Report)
		ingestRes = res
	}

	s.jobMu.Lock()
	prevJob := *targetJob
	targetJob.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	targetJob.WorkerID = req.WorkerID
	targetJob.ClaimToken = ""
	targetJob.LeaseExpiresAt = ""
	targetJob.completing = false
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
	if err := s.saveJobsLocked(); err != nil {
		*targetJob = prevJob
		s.jobMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("persist job completion: %v", err)})
		return
	}
	jobCopy := *targetJob
	s.jobMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"job":    jobCopy,
		"ingest": ingestRes,
	})
}
