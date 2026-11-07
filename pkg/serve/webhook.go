package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/report"
)

// WebhookFormat identifies the payload schema sent to a webhook endpoint.
type WebhookFormat string

const (
	WebhookGeneric WebhookFormat = "generic"
	WebhookSlack   WebhookFormat = "slack"
	WebhookDiscord WebhookFormat = "discord"
)

// WebhookAlert is the structured event payload dispatched when an ingested
// report or campaign contains a consistency violation, regression, or SLA breach.
type WebhookAlert struct {
	Event               string          `json:"event"`
	RunID               string          `json:"id"`
	Kind                string          `json:"kind"`
	Target              string          `json:"target"`
	Model               string          `json:"model"`
	Verdict             checker.Verdict `json:"verdict"`
	Seed                uint64          `json:"seed,omitempty"`
	TotalSeeds          int             `json:"total_seeds"`
	Violations          int             `json:"violations"`
	Regressions         int             `json:"regressions"`
	OverallAvailability float64         `json:"overall_availability,omitempty"`
	MTTRP95Ms           int64           `json:"mttr_p95_ms,omitempty"`
	Fingerprint         string          `json:"fingerprint"`
	Commit              string          `json:"commit,omitempty"`
	Branch              string          `json:"branch,omitempty"`
	Summary             string          `json:"summary"`
	Text                string          `json:"text"`
	Content             string          `json:"content"`
	Timestamp           string          `json:"timestamp"`
}

// WebhookDelivery records the outcome of one webhook HTTP POST attempt.
type WebhookDelivery struct {
	RunID      string        `json:"run_id"`
	Event      string        `json:"event"`
	Format     WebhookFormat `json:"format"`
	URL        string        `json:"url"`
	StatusCode int           `json:"status_code,omitempty"`
	Success    bool          `json:"success"`
	Error      string        `json:"error,omitempty"`
	DurationMs int64         `json:"duration_ms"`
	Timestamp  string        `json:"timestamp"`
}

// ParseWebhookURLs splits comma-separated webhook URLs and trims whitespace.
func ParseWebhookURLs(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		u := strings.TrimSpace(part)
		if u != "" {
			out = append(out, u)
		}
	}
	return out
}

// DetectWebhookFormat inspects a webhook URL (and optional ?format= query
// parameter) to select Slack, Discord, or Generic JSON formatting.
func DetectWebhookFormat(rawURL string) WebhookFormat {
	if u, err := url.Parse(rawURL); err == nil {
		switch strings.ToLower(strings.TrimSpace(u.Query().Get("format"))) {
		case "slack":
			return WebhookSlack
		case "discord":
			return WebhookDiscord
		case "generic", "json":
			return WebhookGeneric
		}
		host := strings.ToLower(u.Host)
		path := strings.ToLower(u.Path)
		if strings.Contains(host, "hooks.slack.com") {
			return WebhookSlack
		}
		if (strings.Contains(host, "discord.com") || strings.Contains(host, "discordapp.com")) &&
			strings.Contains(path, "/api/webhooks") {
			return WebhookDiscord
		}
	}
	return WebhookGeneric
}

func buildAlert(res *IngestResult, rep *report.Report, cfg Config) (*WebhookAlert, bool) {
	if res == nil {
		return nil, false
	}
	e := res.Entry
	var event string
	switch e.Kind {
	case "report":
		if e.Verdict == checker.Violation || e.Violations > 0 {
			event = "report.violation"
		} else if rep != nil && rep.Resilience != nil {
			mttrBreach := cfg.MaxMTTR > 0 && rep.Resilience.MTTR.P95 > cfg.MaxMTTR
			availBreach := cfg.MinAvailability > 0 && rep.Resilience.OverallAvailability < cfg.MinAvailability
			if mttrBreach || availBreach {
				event = "report.sla_breach"
			}
		}
		if event == "" {
			return nil, false
		}
	case "campaign":
		if res.Comparison != nil && (len(res.Comparison.Regressed) > 0 || len(res.Comparison.NewFailures) > 0) {
			event = "campaign.regression"
		} else if e.Violations > 0 || e.Verdict == checker.Violation {
			event = "campaign.violation"
		} else {
			return nil, false
		}
	default:
		return nil, false
	}

	summary := formatAlertSummary(event, e, res, rep, cfg)
	return &WebhookAlert{
		Event:               event,
		RunID:               e.ID,
		Kind:                e.Kind,
		Target:              e.Target,
		Model:               e.Model,
		Verdict:             e.Verdict,
		Seed:                e.Seed,
		TotalSeeds:          e.TotalSeeds,
		Violations:          e.Violations,
		Regressions:         e.Regressions,
		OverallAvailability: e.OverallAvailability,
		MTTRP95Ms:           e.MTTRP95Ms,
		Fingerprint:         e.Fingerprint,
		Commit:              e.Commit,
		Branch:              e.Branch,
		Summary:             summary,
		Text:                summary,
		Content:             summary,
		Timestamp:           e.Timestamp,
	}, true
}

func formatAlertSummary(event string, e RunEntry, res *IngestResult, rep *report.Report, cfg Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[STRIFE ALERT] %s on target %s (run %s, model %s): verdict=%s",
		event, e.Target, e.ID, e.Model, strings.ToUpper(string(e.Verdict)))
	if e.Kind == "report" {
		fmt.Fprintf(&b, ", seed=%d", e.Seed)
		if event == "report.sla_breach" && rep != nil && rep.Resilience != nil {
			fmt.Fprintf(&b, " — SLA breach (availability=%.1f%%, MTTR p95=%dms; thresholds min_avail=%.1f%%, max_mttr=%dms)",
				rep.Resilience.OverallAvailability*100,
				rep.Resilience.MTTR.P95.Milliseconds(),
				cfg.MinAvailability*100,
				cfg.MaxMTTR.Milliseconds())
		} else if rep != nil {
			for _, f := range rep.Check.Failures() {
				if f.Counterexample != nil {
					fmt.Fprintf(&b, " — %s", f.Counterexample.Summary())
					break
				}
			}
		}
	} else {
		fmt.Fprintf(&b, ", violations=%d/%d, regressions=%d", e.Violations, e.TotalSeeds, e.Regressions)
		if res.Comparison != nil && len(res.Comparison.Regressed) > 0 {
			r0 := res.Comparison.Regressed[0]
			fmt.Fprintf(&b, " (seed %d regressed: %s -> %s)", r0.Seed, r0.PrevVerdict, r0.CurrVerdict)
		}
	}
	if e.Commit != "" {
		fmt.Fprintf(&b, " [commit %s", e.Commit)
		if e.Branch != "" {
			fmt.Fprintf(&b, " on %s", e.Branch)
		}
		b.WriteString("]")
	}
	return b.String()
}

func marshalWebhookPayload(format WebhookFormat, a *WebhookAlert) ([]byte, error) {
	switch format {
	case WebhookSlack:
		payload := map[string]any{
			"text": a.Summary,
			"blocks": []map[string]any{
				{
					"type": "section",
					"text": map[string]any{
						"type": "mrkdwn",
						"text": fmt.Sprintf("*Strife Alert (`%s`)*\n%s", a.Event, a.Summary),
					},
				},
			},
		}
		return json.Marshal(payload)
	case WebhookDiscord:
		payload := map[string]any{
			"content": a.Summary,
			"embeds": []map[string]any{
				{
					"title":       fmt.Sprintf("Strife Alert: %s (%s)", a.Target, a.Event),
					"description": a.Summary,
					"color":       0xef4444,
					"fields": []map[string]any{
						{"name": "Run ID", "value": a.RunID, "inline": true},
						{"name": "Target", "value": a.Target, "inline": true},
						{"name": "Verdict", "value": strings.ToUpper(string(a.Verdict)), "inline": true},
					},
				},
			},
		}
		return json.Marshal(payload)
	default:
		return json.Marshal(a)
	}
}

func (s *Server) notifyWebhooks(res *IngestResult, rep *report.Report) []WebhookDelivery {
	if len(s.cfg.Webhooks) == 0 {
		return nil
	}
	alert, ok := buildAlert(res, rep, s.cfg)
	if !ok {
		return nil
	}
	timeout := s.cfg.WebhookTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	deliveries := make([]WebhookDelivery, len(s.cfg.Webhooks))
	var wg sync.WaitGroup
	for i, rawURL := range s.cfg.Webhooks {
		wg.Add(1)
		go func(idx int, targetURL string) {
			defer wg.Done()
			start := time.Now()
			format := DetectWebhookFormat(targetURL)
			d := WebhookDelivery{
				RunID:     alert.RunID,
				Event:     alert.Event,
				Format:    format,
				URL:       redactWebhookURL(targetURL),
				Timestamp: start.UTC().Format(time.RFC3339),
			}
			body, err := marshalWebhookPayload(format, alert)
			if err != nil {
				d.Error = err.Error()
				deliveries[idx] = d
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
			if err != nil {
				d.Error = err.Error()
				deliveries[idx] = d
				return
			}
			req.Header.Set("Content-Type", "application/json; charset=utf-8")
			req.Header.Set("User-Agent", "strife-serve/"+report.Version)
			req.Header.Set("X-Strife-Event", alert.Event)
			resp, err := client.Do(req)
			d.DurationMs = time.Since(start).Milliseconds()
			if err != nil {
				d.Error = err.Error()
				deliveries[idx] = d
				return
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			d.StatusCode = resp.StatusCode
			d.Success = resp.StatusCode >= 200 && resp.StatusCode < 300
			if !d.Success {
				d.Error = fmt.Sprintf("unexpected HTTP status %d", resp.StatusCode)
			}
			deliveries[idx] = d
		}(i, rawURL)
	}
	wg.Wait()

	s.webhookMu.Lock()
	s.deliveries = append(s.deliveries, deliveries...)
	if len(s.deliveries) > 200 {
		s.deliveries = append([]WebhookDelivery(nil), s.deliveries[len(s.deliveries)-200:]...)
	}
	s.webhookMu.Unlock()

	return deliveries
}

func redactWebhookURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.User = nil
	return u.String()
}
