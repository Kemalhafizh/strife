package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/attest"
	"github.com/kemalhafizh/strife/pkg/campaign"
	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/report"
)

func TestServeIngestReportCampaignAndPersistence(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "registry")
	privPath := filepath.Join(tmp, "auditor.key")
	pubPath := filepath.Join(tmp, "auditor.pub")

	pub, priv, err := attest.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := attest.SaveKeys(privPath, pubPath, priv, pub); err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(Config{
		ListenAddr:     "127.0.0.1:0",
		StoreDir:       storeDir,
		Token:          "ci-token-secret",
		SigningKeyPath: privPath,
		Signer:         "ci-bot",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		_ = srv.Shutdown(context.Background())
		_ = ln.Close()
	}()

	baseURL := "http://" + ln.Addr().String()

	// 1. Ingest a single-seed report (with Ed25519 signing enabled).
	v := int64(42)
	ops := []history.Op{
		{Index: 0, Process: 0, Type: history.Invoke, F: history.FWrite, Key: "k0", Value: &v, Time: 10},
		{Index: 1, Process: 0, Type: history.OK, F: history.FWrite, Key: "k0", Value: &v, Time: 20},
	}
	rep, err := report.New(
		report.KindSimulation,
		"raft-ledger",
		7,
		map[string]any{"nodes": 5},
		nemesis.Schedule{Nodes: 5, Duration: 5 * time.Second},
		ops,
		checker.Report{Model: "ledger", Verdict: checker.Linearizable, Operations: 1, Checked: 1},
		"trace-007",
	)
	if err != nil {
		t.Fatal(err)
	}
	repBytes, _ := json.Marshal(rep)

	// Unauthenticated POST must fail with 401.
	unauthResp, err := http.Post(baseURL+"/api/v1/ingest", "application/json", bytes.NewReader(repBytes))
	if err != nil {
		t.Fatal(err)
	}
	unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated ingest, got %d", unauthResp.StatusCode)
	}

	postAuth := func(payload []byte) (*http.Response, []byte) {
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/ingest?commit=abc1234&branch=main", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer ci-token-secret")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}

	resp, body := postAuth(repBytes)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", resp.StatusCode, string(body))
	}
	var res1 IngestResult
	if err := json.Unmarshal(body, &res1); err != nil {
		t.Fatal(err)
	}
	if !res1.Entry.Signed || res1.Bundle == nil {
		t.Fatalf("expected report to be signed by strife serve, got %+v", res1)
	}

	// 2. Ingest baseline campaign (all 2 seeds pass) followed by a regressed campaign (seed 2 violates).
	camp1 := &campaign.Manifest{
		Tool:       "strife",
		Version:    report.Version,
		CampaignID: "camp-base",
		System:     "raft",
		TotalSeeds: 2,
		Passed:     2,
		TotalOps:   200,
		Seeds: []campaign.SeedRecord{
			{Seed: 1, Verdict: checker.Linearizable, Ops: 100, TraceHash: "t1", HistoryHash: "h1", ScheduleHash: "s1"},
			{Seed: 2, Verdict: checker.Linearizable, Ops: 100, TraceHash: "t2", HistoryHash: "h2", ScheduleHash: "s2"},
		},
	}
	camp1.Fingerprint = camp1.ComputeFingerprint()
	c1Bytes, _ := json.Marshal(camp1)
	respC1, bodyC1 := postAuth(c1Bytes)
	if respC1.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 for camp1, got %d: %s", respC1.StatusCode, string(bodyC1))
	}

	camp2 := &campaign.Manifest{
		Tool:       "strife",
		Version:    report.Version,
		CampaignID: "camp-next",
		System:     "raft",
		TotalSeeds: 2,
		Passed:     1,
		Violations: 1,
		TotalOps:   200,
		Seeds: []campaign.SeedRecord{
			{Seed: 1, Verdict: checker.Linearizable, Ops: 100, TraceHash: "t1", HistoryHash: "h1", ScheduleHash: "s1"},
			{Seed: 2, Verdict: checker.Violation, Ops: 100, TraceHash: "t2-bad", HistoryHash: "h2-bad", ScheduleHash: "s2", Explanation: "stale read on k0"},
		},
	}
	camp2.Fingerprint = camp2.ComputeFingerprint()
	c2Bytes, _ := json.Marshal(camp2)
	respC2, bodyC2 := postAuth(c2Bytes)
	if respC2.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 for camp2, got %d: %s", respC2.StatusCode, string(bodyC2))
	}
	var resC2 IngestResult
	if err := json.Unmarshal(bodyC2, &resC2); err != nil {
		t.Fatal(err)
	}
	if resC2.Entry.Regressions != 1 || resC2.Comparison == nil || len(resC2.Comparison.Regressed) != 1 {
		t.Fatalf("expected 1 automatic seed regression on seed 2, got %+v", resC2)
	}

	// 3. Check GET / dashboard and GET /api/v1/runs.
	uiResp, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	uiBytes, _ := io.ReadAll(uiResp.Body)
	uiResp.Body.Close()
	if !strings.Contains(string(uiBytes), "Strife Continuous Verification") || !strings.Contains(string(uiBytes), res1.Entry.ID) {
		t.Fatalf("dashboard HTML missing expected content")
	}

	// 4. Verify persistence by opening a fresh Store on the same directory.
	reloaded, err := NewStore(storeDir)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	if got := len(reloaded.List("", "")); got != 3 {
		t.Fatalf("expected 3 persisted runs after reload, got %d", got)
	}
}

func TestServeWebhooksSlackDiscordAndGeneric(t *testing.T) {
	var mu sync.Mutex
	var received []map[string]any
	var formats []string
	var events []string
	hookLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hookMux := http.NewServeMux()
	hookMux.HandleFunc("/hook", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		received = append(received, m)
		formats = append(formats, r.URL.Query().Get("format"))
		events = append(events, r.Header.Get("X-Strife-Event"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	hookSrv := &http.Server{Handler: hookMux}
	go func() { _ = hookSrv.Serve(hookLn) }()
	defer func() {
		_ = hookSrv.Shutdown(context.Background())
		_ = hookLn.Close()
	}()

	hookBase := "http://" + hookLn.Addr().String() + "/hook"
	webhooks := ParseWebhookURLs(hookBase + ", " + hookBase + "?format=slack, " + hookBase + "?format=discord")

	srv, err := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		StoreDir:   t.TempDir(),
		Webhooks:   webhooks,
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		_ = srv.Shutdown(context.Background())
		_ = ln.Close()
	}()
	baseURL := "http://" + ln.Addr().String()

	// 1. Linearizable report must NOT trigger webhooks.
	v := int64(10)
	ops := []history.Op{
		{Index: 0, Process: 0, Type: history.Invoke, F: history.FWrite, Key: "k0", Value: &v, Time: 10},
		{Index: 1, Process: 0, Type: history.OK, F: history.FWrite, Key: "k0", Value: &v, Time: 20},
	}
	cleanRep, err := report.New(
		report.KindSimulation,
		"raft",
		1,
		map[string]any{"nodes": 5},
		nemesis.Schedule{Nodes: 5, Duration: 5 * time.Second},
		ops,
		checker.Report{Model: "kv", Verdict: checker.Linearizable, Operations: 1, Checked: 1},
		"trace-clean",
	)
	if err != nil {
		t.Fatal(err)
	}
	cleanBytes, _ := json.Marshal(cleanRep)
	resp, err := http.Post(baseURL+"/api/v1/ingest", "application/json", bytes.NewReader(cleanBytes))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(received) != 0 {
		t.Fatalf("expected 0 webhook deliveries for linearizable report, got %d", len(received))
	}

	// 2. Violating report MUST trigger all 3 webhooks (generic, slack, discord).
	badRep, err := report.New(
		report.KindSimulation,
		"2pc-ledger-torn-commit",
		27,
		map[string]any{"nodes": 5},
		nemesis.Schedule{Nodes: 5, Duration: 5 * time.Second},
		ops,
		checker.Report{Model: "ledger", Verdict: checker.Violation, Operations: 1, Checked: 1},
		"trace-bad",
	)
	if err != nil {
		t.Fatal(err)
	}
	badBytes, _ := json.Marshal(badRep)
	resp2, err := http.Post(baseURL+"/api/v1/ingest?commit=deadbeef&branch=main", "application/json", bytes.NewReader(badBytes))
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	var res2 IngestResult
	if err := json.Unmarshal(body2, &res2); err != nil {
		t.Fatal(err)
	}
	if len(res2.Webhooks) != 3 {
		t.Fatalf("expected 3 webhook deliveries in IngestResult, got %d", len(res2.Webhooks))
	}
	for _, d := range res2.Webhooks {
		if !d.Success || d.Event != "report.violation" {
			t.Fatalf("unexpected delivery status: %+v", d)
		}
	}

	// Verify GET /api/v1/webhooks history endpoint.
	whResp, err := http.Get(baseURL + "/api/v1/webhooks")
	if err != nil {
		t.Fatal(err)
	}
	whBytes, _ := io.ReadAll(whResp.Body)
	whResp.Body.Close()
	if !strings.Contains(string(whBytes), "report.violation") {
		t.Fatalf("expected report.violation in /api/v1/webhooks response, got %s", string(whBytes))
	}
}
