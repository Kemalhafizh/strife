package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
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

	// 3. Check GET / dashboard and GET /api/v1/runs require Bearer token and render expected content when authorized.
	unauthUI, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	unauthUI.Body.Close()
	if unauthUI.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated GET /, got %d", unauthUI.StatusCode)
	}
	unauthRuns, err := http.Get(baseURL + "/api/v1/runs")
	if err != nil {
		t.Fatal(err)
	}
	unauthRuns.Body.Close()
	if unauthRuns.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated GET /api/v1/runs, got %d", unauthRuns.StatusCode)
	}

	uiReq, _ := http.NewRequest(http.MethodGet, baseURL+"/", nil)
	uiReq.Header.Set("Authorization", "Bearer ci-token-secret")
	uiResp, err := http.DefaultClient.Do(uiReq)
	if err != nil {
		t.Fatal(err)
	}
	uiBytes, _ := io.ReadAll(uiResp.Body)
	uiResp.Body.Close()
	if uiResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for authenticated GET /, got %d: %s", uiResp.StatusCode, string(uiBytes))
	}
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

func TestServeComplianceDossierEndpointAndSLABreachWebhook(t *testing.T) {
	tmp := t.TempDir()
	privPath := filepath.Join(tmp, "auditor.key")
	pubPath := filepath.Join(tmp, "auditor.pub")
	pub, priv, err := attest.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := attest.SaveKeys(privPath, pubPath, priv, pub); err != nil {
		t.Fatal(err)
	}

	var slaEvents []string
	var mu sync.Mutex
	hookLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hookSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		slaEvents = append(slaEvents, r.Header.Get("X-Strife-Event"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = hookSrv.Serve(hookLn) }()
	defer func() {
		_ = hookSrv.Shutdown(context.Background())
		_ = hookLn.Close()
	}()

	srv, err := NewServer(Config{
		ListenAddr:      "127.0.0.1:0",
		StoreDir:        filepath.Join(tmp, "store"),
		SigningKeyPath:  privPath,
		Signer:          "sla-auditor",
		Webhooks:        []string{"http://" + hookLn.Addr().String() + "/hook"},
		MaxMTTR:         100 * time.Millisecond,
		MinAvailability: 0.95,
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

	// Create a linearizable settlement report whose post-heal recovery took 400ms (> 100ms MaxMTTR).
	txID := int64(101)
	ops := []history.Op{
		{
			Index: 0, Process: 0, Type: history.Invoke, F: history.FTxn, Key: "settlement", Value: &txID, Time: int64(100 * time.Millisecond),
			Txn: []history.MicroOp{{F: history.MicroAppend, Key: "ledger", Value: &txID}, {F: history.MicroAppend, Key: "broker", Value: &txID}},
		},
		{
			Index: 1, Process: 0, Type: history.OK, F: history.FTxn, Key: "settlement", Value: &txID, Time: int64(1400 * time.Millisecond),
			Txn: []history.MicroOp{{F: history.MicroAppend, Key: "ledger", Value: &txID}, {F: history.MicroAppend, Key: "broker", Value: &txID}},
		},
	}
	sched := nemesis.Schedule{
		Nodes:    3,
		Duration: 2 * time.Second,
		Faults: []nemesis.Fault{
			{ID: 1, Kind: nemesis.Partition, Start: 200 * time.Millisecond, End: 1000 * time.Millisecond, Nodes: []int{0}},
		},
	}
	rep, err := report.New(
		report.KindSimulation,
		"outbox-settlement",
		1,
		map[string]any{"nodes": 3},
		sched,
		ops,
		checker.Report{Model: "settlement", Verdict: checker.Linearizable, Operations: 1, Checked: 1},
		"trace-settlement-001",
	)
	if err != nil {
		t.Fatal(err)
	}
	repBytes, _ := json.Marshal(rep)
	resp, err := http.Post(baseURL+"/api/v1/ingest", "application/json", bytes.NewReader(repBytes))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(body))
	}
	var ing IngestResult
	if err := json.Unmarshal(body, &ing); err != nil {
		t.Fatal(err)
	}
	if ing.Entry.MTTRP95Ms < 350 {
		t.Fatalf("expected MTTRP95Ms >= 350ms, got %d", ing.Entry.MTTRP95Ms)
	}
	mu.Lock()
	gotEvents := append([]string(nil), slaEvents...)
	mu.Unlock()
	if len(gotEvents) != 1 || gotEvents[0] != "report.sla_breach" {
		t.Fatalf("expected report.sla_breach webhook alert, got %v", gotEvents)
	}

	// Fetch JSON compliance dossier (/api/v1/runs/<id>/dossier).
	dResp, err := http.Get(baseURL + "/api/v1/runs/" + ing.Entry.ID + "/dossier?frameworks=dora,bi-ojk")
	if err != nil {
		t.Fatal(err)
	}
	dBody, _ := io.ReadAll(dResp.Body)
	dResp.Body.Close()
	if dResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from dossier endpoint, got %d: %s", dResp.StatusCode, string(dBody))
	}
	if !strings.Contains(string(dBody), `"attested": true`) || !strings.Contains(string(dBody), `"bi-ojk.rekonsiliasi-buku-besar"`) {
		t.Fatalf("expected attested dossier with bi-ojk control, got %s", string(dBody))
	}

	// Fetch HTML compliance dossier (/api/v1/runs/<id>/dossier?format=html).
	hResp, err := http.Get(baseURL + "/api/v1/runs/" + ing.Entry.ID + "/dossier?format=html")
	if err != nil {
		t.Fatal(err)
	}
	hBody, _ := io.ReadAll(hResp.Body)
	hResp.Body.Close()
	if hResp.StatusCode != http.StatusOK || !strings.Contains(string(hBody), "Strife Compliance Dossier") {
		t.Fatalf("expected HTML compliance dossier, got status=%d", hResp.StatusCode)
	}
}

func TestServeMultiTenantIsolationSeriesAndPush(t *testing.T) {
	srv, err := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		StoreDir:   t.TempDir(),
		Token:      "push-secret",
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

	// Tenant A baseline campaign (passes seed 1)
	campA := &campaign.Manifest{
		Tool:       "strife",
		Version:    "0.6.0",
		System:     "raft-ledger",
		TotalSeeds: 1,
		Passed:     1,
		Seeds:      []campaign.SeedRecord{{Seed: 1, Verdict: checker.Linearizable, Ops: 50, TraceHash: "t1"}},
	}
	resA, err := Push(context.Background(), baseURL, "push-secret", "bank-alpha", "c1", "main", campA)
	if err != nil {
		t.Fatalf("Push tenant A: %v", err)
	}
	if resA.Entry.Tenant != "bank-alpha" || resA.Entry.Regressions != 0 {
		t.Fatalf("unexpected tenant A entry: %+v", resA.Entry)
	}

	// Tenant B campaign on the SAME system ("raft-ledger") where seed 1 violates:
	// Because Tenant B has no prior baseline of its own, it must NOT be diffed against Tenant A's baseline!
	campB := &campaign.Manifest{
		Tool:       "strife",
		Version:    "0.6.0",
		System:     "raft-ledger",
		TotalSeeds: 1,
		Violations: 1,
		Seeds:      []campaign.SeedRecord{{Seed: 1, Verdict: checker.Violation, Ops: 50, TraceHash: "t2"}},
	}
	resB, err := Push(context.Background(), baseURL, "push-secret", "fintech-beta", "c2", "main", campB)
	if err != nil {
		t.Fatalf("Push tenant B: %v", err)
	}
	if resB.Entry.Tenant != "fintech-beta" || resB.Entry.Regressions != 0 {
		t.Fatalf("tenant B must be isolated from tenant A's baseline, got: %+v", resB.Entry)
	}

	// Query GET /api/v1/series?tenant=bank-alpha requires auth and isolates tenants
	unauthSeries, err := http.Get(baseURL + "/api/v1/series?tenant=bank-alpha")
	if err != nil {
		t.Fatal(err)
	}
	unauthSeries.Body.Close()
	if unauthSeries.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated /api/v1/series, got %d", unauthSeries.StatusCode)
	}
	sReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/series?tenant=bank-alpha", nil)
	sReq.Header.Set("Authorization", "Bearer push-secret")
	sResp, err := http.DefaultClient.Do(sReq)
	if err != nil {
		t.Fatal(err)
	}
	sBody, _ := io.ReadAll(sResp.Body)
	sResp.Body.Close()
	if sResp.StatusCode != http.StatusOK || !strings.Contains(string(sBody), `"tenant": "bank-alpha"`) || strings.Contains(string(sBody), "fintech-beta") {
		t.Fatalf("unexpected /api/v1/series response (status=%d): %s", sResp.StatusCode, string(sBody))
	}
}

func TestServePerTenantKeysAndUIDashboard(t *testing.T) {
	tmp := t.TempDir()
	pubA, privA, err := attest.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pubB, privB, err := attest.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	keyPathA := filepath.Join(tmp, "alpha.key")
	pubPathA := filepath.Join(tmp, "alpha.pub")
	keyPathB := filepath.Join(tmp, "beta.key")
	pubPathB := filepath.Join(tmp, "beta.pub")
	if err := attest.SaveKeys(keyPathA, pubPathA, privA, pubA); err != nil {
		t.Fatal(err)
	}
	if err := attest.SaveKeys(keyPathB, pubPathB, privB, pubB); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(Config{
		ListenAddr: ln.Addr().String(),
		StoreDir:   filepath.Join(tmp, "registry"),
		Signer:     "strife-serve",
		TenantKeyPaths: map[string]string{
			"bank-alpha":   keyPathA,
			"fintech-beta": keyPathB,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		_ = srv.Shutdown(context.Background())
		_ = ln.Close()
	}()
	baseURL := "http://" + ln.Addr().String()

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
	resA, err := Push(context.Background(), baseURL, "", "bank-alpha", "c1", "main", rep)
	if err != nil {
		t.Fatalf("Push bank-alpha: %v", err)
	}
	resB, err := Push(context.Background(), baseURL, "", "fintech-beta", "c2", "main", rep)
	if err != nil {
		t.Fatalf("Push fintech-beta: %v", err)
	}
	if !resA.Entry.Signed || resA.Entry.KeyID != attest.KeyID(pubA) || resA.Entry.Signer != "strife-serve:bank-alpha" {
		t.Fatalf("expected bank-alpha signature with key %s, got %+v", attest.KeyID(pubA), resA.Entry)
	}
	if !resB.Entry.Signed || resB.Entry.KeyID != attest.KeyID(pubB) || resB.Entry.Signer != "strife-serve:fintech-beta" {
		t.Fatalf("expected fintech-beta signature with key %s, got %+v", attest.KeyID(pubB), resB.Entry)
	}

	// Verify GET /api/v1/keys exposes both tenant public keys
	kResp, err := http.Get(baseURL + "/api/v1/keys")
	if err != nil {
		t.Fatal(err)
	}
	kBody, _ := io.ReadAll(kResp.Body)
	kResp.Body.Close()
	if kResp.StatusCode != http.StatusOK || !strings.Contains(string(kBody), attest.KeyID(pubA)) || !strings.Contains(string(kBody), attest.KeyID(pubB)) {
		t.Fatalf("unexpected /api/v1/keys response: %s", string(kBody))
	}

	// Verify GET /api/v1/runs/<id>/dossier validates the tenant signature automatically
	dResp, err := http.Get(baseURL + "/api/v1/runs/" + resA.Entry.ID + "/dossier")
	if err != nil {
		t.Fatal(err)
	}
	dBody, _ := io.ReadAll(dResp.Body)
	dResp.Body.Close()
	if dResp.StatusCode != http.StatusOK || !strings.Contains(string(dBody), `"attested": true`) || !strings.Contains(string(dBody), `"signer": "strife-serve:bank-alpha"`) {
		t.Fatalf("expected verified tenant attestation in dossier, got: %s", string(dBody))
	}

	// Verify GET /?tenant=bank-alpha renders the Time-Series SLA Overview and filters by tenant
	uiResp, err := http.Get(baseURL + "/?tenant=bank-alpha")
	if err != nil {
		t.Fatal(err)
	}
	uiBody, _ := io.ReadAll(uiResp.Body)
	uiResp.Body.Close()
	if uiResp.StatusCode != http.StatusOK || !strings.Contains(string(uiBody), "Target &amp; Tenant Time-Series SLA Overview") || !strings.Contains(string(uiBody), "bank-alpha") {
		t.Fatalf("unexpected dashboard HTML: %s", string(uiBody))
	}
}

func TestRedactWebhookURL(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{
			input: "https://hooks.slack.com/services/T0123/B0456/SECRET_TOKEN_XYZ",
			want:  "https://hooks.slack.com/services/T0123/B0456/redacted",
		},
		{
			input: "https://discord.com/api/webhooks/987654321/SECRET_DISCORD_TOKEN",
			want:  "https://discord.com/api/webhooks/987654321/redacted",
		},
		{
			input: "https://discordapp.com/api/webhooks/987654321/SECRET_DISCORD_TOKEN",
			want:  "https://discordapp.com/api/webhooks/987654321/redacted",
		},
		{
			input: "https://user:password@example.com/alerts?token=MY_SECRET&format=slack",
			want:  "https://example.com/alerts?format=slack&token=redacted",
		},
		{
			input: "http://127.0.0.1:8080/hook?format=slack",
			want:  "http://127.0.0.1:8080/hook?format=slack",
		},
	}

	for _, tc := range cases {
		got := redactWebhookURL(tc.input)
		if got != tc.want {
			t.Errorf("redactWebhookURL(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestServeIngestValidation(t *testing.T) {
	srv, err := NewServer(Config{ListenAddr: "127.0.0.1:0", StoreDir: t.TempDir()})
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

	for _, payload := range []string{"{}", "null", "[]"} {
		resp, err := http.Post(baseURL+"/api/v1/ingest", "application/json", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("payload %s: expected 400 Bad Request, got %d", payload, resp.StatusCode)
		}
	}

	store := srv.store
	if _, err := store.IngestReport(&report.Report{}, "", "", nil, ""); err == nil {
		t.Fatal("expected error for report missing Target")
	}
	if _, err := store.IngestCampaign(&campaign.Manifest{}); err == nil {
		t.Fatal("expected error for campaign missing System")
	}
}

func TestWorkerPullJobQueueAndTrends(t *testing.T) {
	srv, err := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		StoreDir:   t.TempDir(),
		Token:      "vpc-worker-secret",
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

	// 1. Enqueue a simulation job via POST /api/v1/jobs
	jobPayload := []byte(`{"kind":"sim","system":"raft","seed":7,"duration":"2s","faults":"partition","commit":"abc1234","branch":"main"}`)
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/jobs", bytes.NewReader(jobPayload))
	req.Header.Set("Authorization", "Bearer vpc-worker-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created for job enqueue, got %d: %s", resp.StatusCode, string(body))
	}
	var created Job
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Status != JobPending {
		t.Fatalf("unexpected created job: %+v", created)
	}

	// 2. Run pull-based worker once
	n, err := RunWorker(context.Background(), WorkerConfig{
		ServerURL: baseURL,
		Token:     "vpc-worker-secret",
		WorkerID:  "staging-vpc-worker-1",
		Once:      true,
	})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected worker to process 1 job, got %d", n)
	}

	// 3. Verify job status is completed and linked to ingested RunID (requires auth)
	authGet := func(url, tok string) (*http.Response, []byte) {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}
	jobResp, jobBody := authGet(baseURL+"/api/v1/jobs/"+created.ID, "vpc-worker-secret")
	if jobResp.StatusCode != http.StatusOK {
		t.Fatalf("GET job status=%d body=%s", jobResp.StatusCode, string(jobBody))
	}
	var completed Job
	if err := json.Unmarshal(jobBody, &completed); err != nil {
		t.Fatal(err)
	}
	if completed.Status != JobCompleted || completed.RunID == "" || completed.Verdict != checker.Linearizable {
		t.Fatalf("expected completed linearizable job with RunID, got %+v", completed)
	}

	// 4. Verify GET /api/v1/trends returns the ingested run with resilience metrics
	trResp, trBody := authGet(baseURL+"/api/v1/trends?target=raft", "vpc-worker-secret")
	if trResp.StatusCode != http.StatusOK {
		t.Fatalf("GET trends status=%d body=%s", trResp.StatusCode, string(trBody))
	}
	var trData struct {
		Total  int          `json:"total"`
		Points []TrendPoint `json:"points"`
	}
	if err := json.Unmarshal(trBody, &trData); err != nil {
		t.Fatal(err)
	}
	if trData.Total != 1 || len(trData.Points) != 1 || trData.Points[0].ID != completed.RunID {
		t.Fatalf("unexpected trends response: %+v", trData)
	}
	if trData.Points[0].OverallAvailability <= 0 {
		t.Fatalf("expected positive OverallAvailability in trend point, got %+v", trData.Points[0])
	}

	// 5. Verify re-completing an already-completed job is rejected with 409 Conflict
	replayReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/jobs/"+created.ID+"/complete", strings.NewReader(`{"worker_id":"staging-vpc-worker-1"}`))
	replayReq.Header.Set("Authorization", "Bearer vpc-worker-secret")
	replayResp, err := http.DefaultClient.Do(replayReq)
	if err != nil {
		t.Fatal(err)
	}
	replayResp.Body.Close()
	if replayResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict when re-completing job, got %d", replayResp.StatusCode)
	}

	// 6. Verify unauthenticated GET /api/v1/jobs/<id> is rejected with 401 and does not leak live_config
	runJobPayload := []byte(`{"kind":"run","tenant":"payments","live_config":{"name":"pg-ha","protocol":"postgres","nodes":[{"id":0,"addr":"10.0.0.1:5432"}]}}`)
	runReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/jobs", bytes.NewReader(runJobPayload))
	runReq.Header.Set("Authorization", "Bearer vpc-worker-secret")
	runResp, err := http.DefaultClient.Do(runReq)
	if err != nil {
		t.Fatal(err)
	}
	runBody, _ := io.ReadAll(runResp.Body)
	runResp.Body.Close()
	var createdRunJob Job
	if err := json.Unmarshal(runBody, &createdRunJob); err != nil {
		t.Fatal(err)
	}

	unauthResp, unauthBody := authGet(baseURL+"/api/v1/jobs/"+createdRunJob.ID, "")
	if unauthResp.StatusCode != http.StatusUnauthorized || strings.Contains(string(unauthBody), "10.0.0.1:5432") {
		t.Fatalf("expected 401 without live_config leak on unauthenticated GET /api/v1/jobs/<id>, got status=%d body=%s", unauthResp.StatusCode, string(unauthBody))
	}

	authGetResp, authGetBody := authGet(baseURL+"/api/v1/jobs/"+createdRunJob.ID, "vpc-worker-secret")
	if authGetResp.StatusCode != http.StatusOK || !strings.Contains(string(authGetBody), "10.0.0.1:5432") {
		t.Fatalf("expected authenticated GET /api/v1/jobs/<id> to include live_config, got status=%d body=%s", authGetResp.StatusCode, string(authGetBody))
	}
}

func TestServeHardeningValidationJobPermissionsAndUI(t *testing.T) {
	tmp := t.TempDir()
	if _, err := NewServer(Config{StoreDir: filepath.Join(tmp, "neg-mttr"), MaxMTTR: -time.Second}); err == nil {
		t.Fatal("expected NewServer to reject negative MaxMTTR")
	}
	if _, err := NewServer(Config{StoreDir: filepath.Join(tmp, "bad-avail-low"), MinAvailability: -0.1}); err == nil {
		t.Fatal("expected NewServer to reject negative MinAvailability")
	}
	if _, err := NewServer(Config{StoreDir: filepath.Join(tmp, "bad-avail-high"), MinAvailability: 101}); err == nil {
		t.Fatal("expected NewServer to reject MinAvailability > 100")
	}
	if _, err := NewServer(Config{StoreDir: filepath.Join(tmp, "neg-lease"), JobLease: -time.Second}); err == nil {
		t.Fatal("expected NewServer to reject negative JobLease")
	}

	corruptDir := filepath.Join(tmp, "corrupt-jobs")
	if err := os.MkdirAll(filepath.Join(corruptDir, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, "jobs.json"), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(Config{StoreDir: corruptDir}); err == nil {
		t.Fatal("expected NewServer to fail on corrupt jobs.json")
	}

	storeDir := filepath.Join(tmp, "srv")
	srv, err := NewServer(Config{
		ListenAddr:      "127.0.0.1:0",
		StoreDir:        storeDir,
		Token:           "sec",
		MinAvailability: 95,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if srv.cfg.MinAvailability != 0.95 {
		t.Fatalf("expected MinAvailability 95 to normalize to 0.95, got %v", srv.cfg.MinAvailability)
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

	authGet := func(path string) (*http.Response, []byte) {
		req, _ := http.NewRequest(http.MethodGet, baseURL+path, nil)
		req.Header.Set("Authorization", "Bearer sec")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}

	// Enqueue jobs for two tenants and verify WorkerConfig.Tenant scoping + jobs.json mode 0600.
	for _, payload := range []string{
		`{"kind":"sim","tenant":"billing","system":"raft","seed":1,"duration":"500ms"}`,
		`{"kind":"sim","tenant":"payments","system":"raft-ledger","seed":2,"duration":"500ms"}`,
	} {
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/jobs", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer sec")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("enqueue status = %d", resp.StatusCode)
		}
	}

	info, err := os.Stat(filepath.Join(storeDir, "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected jobs.json mode 0600, got %04o", perm)
	}

	// Claim validation: malformed JSON and oversized body.
	badClaimReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/jobs/claim", strings.NewReader("{bad"))
	badClaimReq.Header.Set("Authorization", "Bearer sec")
	badClaimResp, err := http.DefaultClient.Do(badClaimReq)
	if err != nil {
		t.Fatal(err)
	}
	badClaimResp.Body.Close()
	if badClaimResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed claim body, got %d", badClaimResp.StatusCode)
	}

	bigClaim := strings.Repeat("a", 65*1024)
	bigReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/jobs/claim", strings.NewReader(bigClaim))
	bigReq.Header.Set("Authorization", "Bearer sec")
	bigResp, err := http.DefaultClient.Do(bigReq)
	if err != nil {
		t.Fatal(err)
	}
	bigResp.Body.Close()
	if bigResp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized claim body, got %d", bigResp.StatusCode)
	}

	// Tenant-scoped worker only processes the "payments" job.
	n, err := RunWorker(context.Background(), WorkerConfig{
		ServerURL: baseURL,
		Token:     "sec",
		WorkerID:  "w-pay",
		Tenant:    "payments",
		Once:      true,
	})
	if err != nil || n != 1 {
		t.Fatalf("RunWorker tenant=payments got n=%d err=%v", n, err)
	}
	listResp, listBody := authGet("/api/v1/jobs?tenant=billing")
	if listResp.StatusCode != http.StatusOK || !strings.Contains(string(listBody), `"status": "pending"`) {
		t.Fatalf("expected billing job to remain pending, got status=%d body=%s", listResp.StatusCode, string(listBody))
	}

	// Reload server from storeDir and verify job persistence.
	reloadedSrv, err := NewServer(Config{StoreDir: storeDir})
	if err != nil {
		t.Fatalf("reload NewServer: %v", err)
	}
	if len(reloadedSrv.jobs) != 2 || reloadedSrv.jobSeq != 2 {
		t.Fatalf("expected 2 reloaded jobs and seq=2, got len=%d seq=%d", len(reloadedSrv.jobs), reloadedSrv.jobSeq)
	}

	// Check combined filter links on GET /?target=raft-ledger&tenant=payments
	uiResp, uiBody := authGet("/?target=raft-ledger&tenant=payments")
	if uiResp.StatusCode != http.StatusOK || !strings.Contains(string(uiBody), "target=raft-ledger&amp;tenant=payments") {
		t.Fatalf("expected UI filter pills to preserve combined target and tenant, got status=%d:\n%s", uiResp.StatusCode, string(uiBody))
	}

	// Query param validation + corrupt bundle check on /api/v1/runs/<id>/dossier.
	runs := srv.store.List("", "", "payments")
	if len(runs) != 1 {
		t.Fatalf("expected 1 ingested run for payments, got %d", len(runs))
	}
	runID := runs[0].ID
	for _, q := range []string{"max_mttr=-1s", "max_mttr=nope", "min_availability=101", "min_availability=nope"} {
		dResp, _ := authGet("/api/v1/runs/" + runID + "/dossier?" + q)
		if dResp.StatusCode != http.StatusBadRequest {
			t.Fatalf("query %s: expected 400, got %d", q, dResp.StatusCode)
		}
	}

	if err := os.WriteFile(filepath.Join(storeDir, "runs", runID, "bundle.json"), []byte("{corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	errResp, _ := authGet("/api/v1/runs/" + runID + "/dossier")
	if errResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 on corrupt bundle.json, got %d", errResp.StatusCode)
	}
}

func TestServeAuthEnforcementAndTenantTokenIsolation(t *testing.T) {
	tmp := t.TempDir()
	// Reject ambiguous / empty / duplicate tokens across admin and tenants.
	if _, err := NewServer(Config{StoreDir: filepath.Join(tmp, "dup1"), Token: "shared", TenantTokens: map[string]string{"alpha": "shared"}}); err == nil {
		t.Fatal("expected error when tenant token duplicates admin token")
	}
	if _, err := NewServer(Config{StoreDir: filepath.Join(tmp, "dup2"), TenantTokens: map[string]string{"alpha": "same", "beta": "same"}}); err == nil {
		t.Fatal("expected error when two tenants share the same token")
	}
	if _, err := NewServer(Config{StoreDir: filepath.Join(tmp, "empty-k"), TenantTokens: map[string]string{"": "tok"}}); err == nil {
		t.Fatal("expected error for empty tenant key")
	}
	if _, err := NewServer(Config{StoreDir: filepath.Join(tmp, "empty-v"), TenantTokens: map[string]string{"alpha": ""}}); err == nil {
		t.Fatal("expected error for empty tenant token")
	}

	srv, err := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		StoreDir:   filepath.Join(tmp, "reg"),
		Token:      "admin-secret",
		TenantTokens: map[string]string{
			"alpha": "alpha-secret",
			"beta":  "beta-secret",
		},
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

	doReq := func(method, path, tok, body string) (int, []byte) {
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, baseURL+path, rdr)
		if err != nil {
			t.Fatal(err)
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	makeReport := func(target string) *report.Report {
		v := int64(1)
		ops := []history.Op{
			{Index: 0, Process: 0, Type: history.Invoke, F: history.FWrite, Key: "k0", Value: &v, Time: 10},
			{Index: 1, Process: 0, Type: history.OK, F: history.FWrite, Key: "k0", Value: &v, Time: 20},
		}
		rep, err := report.New(
			report.KindSimulation,
			target,
			1,
			map[string]any{"nodes": 3},
			nemesis.Schedule{Nodes: 3, Duration: time.Second},
			ops,
			checker.Report{Model: "kv", Verdict: checker.Linearizable, Operations: 1, Checked: 1},
			"trace-"+target,
		)
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}

	// Push reports as alpha and beta (omitting ?tenant binds to token's own tenant).
	resA, err := Push(context.Background(), baseURL, "alpha-secret", "", "c1", "main", makeReport("target-alpha"))
	if err != nil || resA.Entry.Tenant != "alpha" {
		t.Fatalf("alpha push failed: res=%+v err=%v", resA, err)
	}
	resB, err := Push(context.Background(), baseURL, "beta-secret", "", "c2", "main", makeReport("target-beta"))
	if err != nil || resB.Entry.Tenant != "beta" {
		t.Fatalf("beta push failed: res=%+v err=%v", resB, err)
	}

	// Cross-tenant push is rejected with 403.
	rawB, _ := json.Marshal(makeReport("target-cross"))
	if code, _ := doReq(http.MethodPost, "/api/v1/ingest?tenant=beta", "alpha-secret", string(rawB)); code != http.StatusForbidden {
		t.Fatalf("expected 403 on cross-tenant ingest, got %d", code)
	}

	// Enqueue one job per tenant.
	code, jobABody := doReq(http.MethodPost, "/api/v1/jobs", "alpha-secret", `{"kind":"sim","system":"raft","seed":1,"duration":"500ms"}`)
	if code != http.StatusCreated {
		t.Fatalf("alpha enqueue failed: %d %s", code, string(jobABody))
	}
	var jobA Job
	_ = json.Unmarshal(jobABody, &jobA)
	if jobA.Tenant != "alpha" {
		t.Fatalf("expected empty tenant on enqueue to bind to alpha, got %+v", jobA)
	}
	code, jobBBody := doReq(http.MethodPost, "/api/v1/jobs", "beta-secret", `{"kind":"sim","system":"raft","seed":2,"duration":"500ms"}`)
	if code != http.StatusCreated {
		t.Fatalf("beta enqueue failed: %d %s", code, string(jobBBody))
	}
	var jobB Job
	_ = json.Unmarshal(jobBBody, &jobB)

	// Public /api/v1/health and /api/v1/keys stay 200 without auth, but omit tenant/target lists unauthenticated.
	if code, hBody := doReq(http.MethodGet, "/api/v1/health", "", ""); code != http.StatusOK || strings.Contains(string(hBody), "target-beta") || strings.Contains(string(hBody), "alpha") {
		t.Fatalf("unauthenticated /api/v1/health leaked tenant/target metadata: code=%d body=%s", code, string(hBody))
	}
	if code, _ := doReq(http.MethodGet, "/api/v1/keys", "", ""); code != http.StatusOK {
		t.Fatalf("expected 200 on public /api/v1/keys, got %d", code)
	}

	// All sensitive endpoints reject unauthenticated callers with 401.
	sensitive := []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/api/v1/runs"},
		{http.MethodGet, "/api/v1/runs/" + resA.Entry.ID},
		{http.MethodGet, "/api/v1/runs/" + resA.Entry.ID + "/dossier"},
		{http.MethodGet, "/api/v1/series"},
		{http.MethodGet, "/api/v1/trends"},
		{http.MethodGet, "/api/v1/webhooks"},
		{http.MethodGet, "/api/v1/jobs"},
		{http.MethodGet, "/api/v1/jobs/" + jobA.ID},
		{http.MethodPost, "/api/v1/jobs/claim"},
	}
	for _, ep := range sensitive {
		if c, _ := doReq(ep.method, ep.path, "", ""); c != http.StatusUnauthorized {
			t.Errorf("%s %s without token: want 401, got %d", ep.method, ep.path, c)
		}
	}

	// Alpha cannot read Beta's runs, dossier, UI, series, trends, jobs, or webhooks.
	forbiddenForAlpha := []struct{ method, path, body string }{
		{http.MethodGet, "/?tenant=beta", ""},
		{http.MethodGet, "/api/v1/runs?tenant=beta", ""},
		{http.MethodGet, "/api/v1/runs/" + resB.Entry.ID, ""},
		{http.MethodGet, "/api/v1/runs/" + resB.Entry.ID + "/dossier", ""},
		{http.MethodGet, "/api/v1/series?tenant=beta", ""},
		{http.MethodGet, "/api/v1/trends?tenant=beta", ""},
		{http.MethodGet, "/api/v1/webhooks", ""},
		{http.MethodGet, "/api/v1/jobs?tenant=beta", ""},
		{http.MethodPost, "/api/v1/jobs", `{"tenant":"beta","kind":"sim"}`},
		{http.MethodGet, "/api/v1/jobs/" + jobB.ID, ""},
		{http.MethodPost, "/api/v1/jobs/claim", `{"worker_id":"w","tenant":"beta"}`},
		{http.MethodPost, "/api/v1/jobs/" + jobB.ID + "/heartbeat", `{"worker_id":"w","claim_token":"x"}`},
		{http.MethodPost, "/api/v1/jobs/" + jobB.ID + "/complete", `{"worker_id":"w"}`},
	}
	for _, ep := range forbiddenForAlpha {
		if c, _ := doReq(ep.method, ep.path, "alpha-secret", ep.body); c != http.StatusForbidden {
			t.Errorf("%s %s with alpha token: want 403, got %d", ep.method, ep.path, c)
		}
	}

	// Alpha reads its own UI, runs, series, trends without any leakage of beta's targets/tenants.
	for _, path := range []string{"/", "/api/v1/runs", "/api/v1/series", "/api/v1/trends", "/api/v1/jobs"} {
		c, b := doReq(http.MethodGet, path, "alpha-secret", "")
		if c != http.StatusOK {
			t.Fatalf("GET %s as alpha: status=%d body=%s", path, c, string(b))
		}
		if strings.Contains(string(b), "target-beta") || strings.Contains(string(b), resB.Entry.ID) || strings.Contains(string(b), jobB.ID) {
			t.Fatalf("GET %s as alpha leaked beta resource: %s", path, string(b))
		}
	}

	// Admin can read webhook history and both tenants' runs.
	if c, _ := doReq(http.MethodGet, "/api/v1/webhooks", "admin-secret", ""); c != http.StatusOK {
		t.Fatalf("admin GET /api/v1/webhooks failed: %d", c)
	}
	if c, b := doReq(http.MethodGet, "/api/v1/runs", "admin-secret", ""); c != http.StatusOK || !strings.Contains(string(b), resA.Entry.ID) || !strings.Contains(string(b), resB.Entry.ID) {
		t.Fatalf("admin GET /api/v1/runs missing both tenants: %d %s", c, string(b))
	}

	// Claim token is returned on /claim, never exposed on GET /jobs or GET /jobs/<id>.
	c, claimBody := doReq(http.MethodPost, "/api/v1/jobs/claim", "alpha-secret", `{"worker_id":"alpha-w1"}`)
	if c != http.StatusOK {
		t.Fatalf("alpha claim failed: %d %s", c, string(claimBody))
	}
	var claimedA Job
	_ = json.Unmarshal(claimBody, &claimedA)
	if claimedA.ID != jobA.ID || claimedA.ClaimToken == "" {
		t.Fatalf("expected alpha worker to claim alpha's job with non-empty claim_token, got %+v", claimedA)
	}
	if _, listBody := doReq(http.MethodGet, "/api/v1/jobs", "admin-secret", ""); strings.Contains(string(listBody), claimedA.ClaimToken) {
		t.Fatal("GET /api/v1/jobs leaked claim_token")
	}
	if _, itemBody := doReq(http.MethodGet, "/api/v1/jobs/"+jobA.ID, "admin-secret", ""); strings.Contains(string(itemBody), claimedA.ClaimToken) {
		t.Fatal("GET /api/v1/jobs/<id> leaked claim_token")
	}
}

func TestServeJobLeaseHeartbeatReclaimAndTOCTOU(t *testing.T) {
	var hookCount int
	var hookMu sync.Mutex
	hookLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hookSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hookMu.Lock()
		hookCount++
		hookMu.Unlock()
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = hookSrv.Serve(hookLn) }()
	defer func() {
		_ = hookSrv.Shutdown(context.Background())
		_ = hookLn.Close()
	}()

	srv, err := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		StoreDir:   t.TempDir(),
		Token:      "lease-sec",
		Webhooks:   []string{"http://" + hookLn.Addr().String() + "/hook"},
		JobLease:   120 * time.Millisecond,
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

	postJSON := func(path, payload string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodPost, baseURL+path, strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer lease-sec")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	c, jBody := postJSON("/api/v1/jobs", `{"kind":"sim","system":"raft","seed":1,"duration":"500ms"}`)
	if c != http.StatusCreated {
		t.Fatalf("enqueue status=%d body=%s", c, string(jBody))
	}
	var job Job
	_ = json.Unmarshal(jBody, &job)

	// Completing a pending job must fail with 409 Conflict.
	if c, _ := postJSON("/api/v1/jobs/"+job.ID+"/complete", `{"worker_id":"w1"}`); c != http.StatusConflict {
		t.Fatalf("expected 409 completing pending job, got %d", c)
	}

	// Worker 1 claims the job.
	c, cBody1 := postJSON("/api/v1/jobs/claim", `{"worker_id":"w1"}`)
	if c != http.StatusOK {
		t.Fatalf("claim w1 status=%d body=%s", c, string(cBody1))
	}
	var claim1 Job
	_ = json.Unmarshal(cBody1, &claim1)
	if claim1.Attempts != 1 || claim1.ClaimToken == "" || claim1.LeaseExpiresAt == "" {
		t.Fatalf("unexpected initial claim: %+v", claim1)
	}

	// Wrong/missing claim_token on heartbeat or complete must return 409 Conflict.
	if c, _ := postJSON("/api/v1/jobs/"+job.ID+"/heartbeat", `{"worker_id":"w1","claim_token":"wrong"}`); c != http.StatusConflict {
		t.Fatalf("expected 409 for wrong claim_token heartbeat, got %d", c)
	}
	if c, _ := postJSON("/api/v1/jobs/"+job.ID+"/complete", `{"worker_id":"w1","claim_token":"wrong"}`); c != http.StatusConflict {
		t.Fatalf("expected 409 for wrong claim_token complete, got %d", c)
	}

	// Renew lease across > 120ms; second worker must receive 204 No Content while heartbeats keep lease alive.
	for i := 0; i < 3; i++ {
		time.Sleep(55 * time.Millisecond)
		hbPayload, _ := json.Marshal(JobHeartbeatRequest{WorkerID: "w1", ClaimToken: claim1.ClaimToken})
		if c, _ := postJSON("/api/v1/jobs/"+job.ID+"/heartbeat", string(hbPayload)); c != http.StatusOK {
			t.Fatalf("heartbeat %d failed: %d", i, c)
		}
	}
	if c, _ := postJSON("/api/v1/jobs/claim", `{"worker_id":"w2"}`); c != http.StatusNoContent {
		t.Fatalf("expected active lease to block w2 reclaim (204), got %d", c)
	}

	// Let lease expire, then w2 reclaims the same job with attempt=2 and a new claim token.
	time.Sleep(150 * time.Millisecond)
	c, cBody2 := postJSON("/api/v1/jobs/claim", `{"worker_id":"w2"}`)
	if c != http.StatusOK {
		t.Fatalf("expected w2 reclaim after lease expiry, got status=%d body=%s", c, string(cBody2))
	}
	var claim2 Job
	_ = json.Unmarshal(cBody2, &claim2)
	if claim2.ID != job.ID || claim2.Attempts != 2 || claim2.ClaimToken == "" || claim2.ClaimToken == claim1.ClaimToken {
		t.Fatalf("unexpected reclaimed job metadata: %+v (prev token %q)", claim2, claim1.ClaimToken)
	}

	// Zombie worker w1 cannot heartbeat or complete after reclaim.
	staleComplete, _ := json.Marshal(JobCompleteRequest{WorkerID: "w1", ClaimToken: claim1.ClaimToken})
	if c, _ := postJSON("/api/v1/jobs/"+job.ID+"/complete", string(staleComplete)); c != http.StatusConflict {
		t.Fatalf("expected 409 for stale w1 completion, got %d", c)
	}

	// Concurrent /complete calls with valid w2 credentials must execute ingest/webhook exactly once.
	v := int64(1)
	ops := []history.Op{
		{Index: 0, Process: 0, Type: history.Invoke, F: history.FWrite, Key: "k0", Value: &v, Time: 10},
		{Index: 1, Process: 0, Type: history.OK, F: history.FWrite, Key: "k0", Value: &v, Time: 20},
	}
	badRep, err := report.New(report.KindSimulation, "raft-viol", 9, map[string]any{"nodes": 3}, nemesis.Schedule{Nodes: 3, Duration: time.Second}, ops, checker.Report{Model: "kv", Verdict: checker.Violation, Operations: 1, Checked: 1}, "t9")
	if err != nil {
		t.Fatal(err)
	}
	completeBody, _ := json.Marshal(JobCompleteRequest{WorkerID: "w2", ClaimToken: claim2.ClaimToken, Report: badRep})

	const racing = 4
	codes := make(chan int, racing)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racing; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			code, _ := postJSON("/api/v1/jobs/"+job.ID+"/complete", string(completeBody))
			codes <- code
		}()
	}
	close(start)
	wg.Wait()
	close(codes)

	okCount, conflictCount := 0, 0
	for code := range codes {
		switch code {
		case http.StatusOK:
			okCount++
		case http.StatusConflict:
			conflictCount++
		default:
			t.Fatalf("unexpected status code in concurrent complete race: %d", code)
		}
	}
	if okCount != 1 || conflictCount != racing-1 {
		t.Fatalf("expected exactly 1 200 OK and %d 409 Conflicts, got ok=%d conflict=%d", racing-1, okCount, conflictCount)
	}
	if runs := srv.Store().List("", ""); len(runs) != 1 {
		t.Fatalf("expected exactly 1 ingested run from concurrent completes, got %d", len(runs))
	}
	hookMu.Lock()
	gotHooks := hookCount
	hookMu.Unlock()
	if gotHooks != 1 {
		t.Fatalf("expected exactly 1 webhook delivery from concurrent completes, got %d", gotHooks)
	}
}

func TestServeStoreTornRegistryRecovery(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	v := int64(7)
	ops := []history.Op{
		{Index: 0, Process: 0, Type: history.Invoke, F: history.FWrite, Key: "k0", Value: &v, Time: 10},
		{Index: 1, Process: 0, Type: history.OK, F: history.FWrite, Key: "k0", Value: &v, Time: 20},
	}
	rep1, err := report.New(report.KindSimulation, "raft", 1, map[string]any{"nodes": 3}, nemesis.Schedule{Nodes: 3, Duration: time.Second}, ops, checker.Report{Model: "kv", Verdict: checker.Linearizable, Operations: 1, Checked: 1}, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.IngestReport(rep1, "c1", "main", nil, "", "t1"); err != nil {
		t.Fatal(err)
	}

	// Simulate crash leaving a partial unterminated JSON line at EOF of registry.jsonl.
	regPath := filepath.Join(dir, "registry.jsonl")
	f, err := os.OpenFile(regPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"run-torn","target":"crash`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen store after torn tail: %v", err)
	}
	if runs := reopened.List("", ""); len(runs) != 1 {
		t.Fatalf("expected 1 valid run after torn tail repair, got %d", len(runs))
	}

	rep2, err := report.New(report.KindSimulation, "raft", 2, map[string]any{"nodes": 3}, nemesis.Schedule{Nodes: 3, Duration: time.Second}, ops, checker.Report{Model: "kv", Verdict: checker.Linearizable, Operations: 1, Checked: 1}, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.IngestReport(rep2, "c2", "main", nil, "", "t1"); err != nil {
		t.Fatal(err)
	}

	reopenedAgain, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen store after post-recovery ingest: %v", err)
	}
	if runs := reopenedAgain.List("", ""); len(runs) != 2 {
		t.Fatalf("expected 2 valid runs persisted cleanly after torn tail recovery, got %d", len(runs))
	}
}
