package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/campaign"
	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/explore"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/probe"
	"github.com/kemalhafizh/strife/pkg/report"
	"github.com/kemalhafizh/strife/pkg/runner"
)

// WorkerConfig configures a pull-based remote strife worker.
type WorkerConfig struct {
	ServerURL    string
	Token        string
	WorkerID     string
	PollInterval time.Duration
	Once         bool
	HTTPClient   *http.Client
	Logf         func(string, ...any)
}

// RunWorker polls ServerURL for pending verification jobs, executes them
// locally (simulation, multi-seed campaign, or live cluster run), and posts
// the resulting report or campaign manifest back to the registry. It returns
// the number of jobs processed.
func RunWorker(ctx context.Context, cfg WorkerConfig) (int, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
	if baseURL == "" {
		return 0, fmt.Errorf("worker: server URL is required")
	}
	if cfg.WorkerID == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = "local"
		}
		cfg.WorkerID = fmt.Sprintf("worker-%s-%d", host, os.Getpid())
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	processed := 0
	for {
		if err := ctx.Err(); err != nil {
			return processed, nil
		}
		job, err := claimNextJob(ctx, client, baseURL, cfg.Token, cfg.WorkerID)
		if err != nil {
			if cfg.Once {
				return processed, err
			}
			logf("worker claim error: %v", err)
		} else if job != nil {
			logf("claimed job %s (kind=%s, system=%s)", job.ID, job.Kind, job.System)
			compReq := executeJob(ctx, job, cfg.WorkerID)
			if err := completeJob(ctx, client, baseURL, cfg.Token, job.ID, compReq); err != nil {
				return processed, fmt.Errorf("worker complete %s: %w", job.ID, err)
			}
			processed++
			logf("completed job %s", job.ID)
			if cfg.Once {
				return processed, nil
			}
			continue
		} else if cfg.Once {
			return processed, nil
		}

		select {
		case <-ctx.Done():
			return processed, nil
		case <-time.After(cfg.PollInterval):
		}
	}
}

func claimNextJob(ctx context.Context, client *http.Client, baseURL, token, workerID string) (*Job, error) {
	body, _ := json.Marshal(JobClaimRequest{WorkerID: workerID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/jobs/claim", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("claim HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var job Job
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, err
	}
	return &job, nil
}

func completeJob(ctx context.Context, client *http.Client, baseURL, token, jobID string, payload JobCompleteRequest) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/jobs/"+jobID+"/complete", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("complete HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

func executeJob(ctx context.Context, job *Job, workerID string) JobCompleteRequest {
	out := JobCompleteRequest{WorkerID: workerID}
	switch job.Kind {
	case "sim":
		cfg, err := buildExploreConfig(job)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		res, err := explore.Run(cfg)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		if sys, err := explore.LookupSystem(cfg.System); err == nil {
			_ = checker.AddWitnesses(sys.Model(), res.History, &res.Check, checker.WitnessOptions{})
		}
		rep, err := report.New(report.KindSimulation, cfg.System, cfg.Seed, cfg, res.Schedule, res.History, res.Check, res.TraceHash)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		rep.Events = res.Events
		rep.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
		rep.Fingerprint = report.Fingerprint(rep)
		out.Report = rep

	case "campaign":
		cfg, err := buildExploreConfig(job)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		seedsStr := job.Seeds
		if seedsStr == "" {
			seedsStr = "1..20"
		}
		seedList, err := explore.ParseSeeds(seedsStr)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		m, err := campaign.Run(ctx, campaign.Options{
			BaseConfig: cfg,
			Seeds:      seedList,
			Commit:     job.Commit,
			Branch:     job.Branch,
		})
		if err != nil {
			out.Error = err.Error()
			return out
		}
		out.Campaign = m

	case "run":
		var rcfg runner.Config
		if err := json.Unmarshal(job.LiveConfig, &rcfg); err != nil {
			out.Error = fmt.Sprintf("invalid live_config: %v", err)
			return out
		}
		if job.Seed > 0 {
			rcfg.Seed = job.Seed
		}
		if err := rcfg.Validate(); err != nil {
			out.Error = err.Error()
			return out
		}
		res, err := runner.Run(ctx, &rcfg, runner.Options{})
		if err != nil {
			out.Error = err.Error()
			return out
		}
		_ = checker.AddWitnesses(rcfg.Model(), res.History, &res.Check, checker.WitnessOptions{})
		res.Findings = probe.Correlate(res.History, res.Check, res.Schedule, res.Probes)
		rep, err := report.New(report.KindLive, rcfg.Name, rcfg.Seed, rcfg.Redacted(), res.Schedule, res.History, res.Check, "")
		if err != nil {
			out.Error = err.Error()
			return out
		}
		rep.Applied = res.Applied
		rep.Resilience = report.ComputeResilience(res.Schedule, res.Applied, res.History)
		rep.Probes = res.Probes
		rep.Findings = res.Findings
		rep.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
		rep.Fingerprint = report.Fingerprint(rep)
		out.Report = rep

	default:
		out.Error = fmt.Sprintf("unsupported job kind %q", job.Kind)
	}
	return out
}

func buildExploreConfig(job *Job) (explore.Config, error) {
	cfg := explore.DefaultConfig()
	if job.System != "" {
		cfg.System = job.System
	}
	if job.Seed > 0 {
		cfg.Seed = job.Seed
	}
	if job.Nodes > 0 {
		cfg.Nodes = job.Nodes
	}
	if job.Clients > 0 {
		cfg.Clients = job.Clients
	}
	if job.Duration != "" {
		d, err := time.ParseDuration(job.Duration)
		if err != nil {
			return explore.Config{}, fmt.Errorf("invalid duration %q: %w", job.Duration, err)
		}
		cfg.Duration = d
	}
	if job.Faults != "" {
		kinds, err := nemesis.ParseKinds(job.Faults)
		if err != nil {
			return explore.Config{}, err
		}
		cfg.Faults = kinds
	}
	return cfg, cfg.Validate()
}
