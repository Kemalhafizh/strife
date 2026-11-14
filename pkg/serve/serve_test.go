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

	// Query GET /api/v1/series?tenant=bank-alpha
	sResp, err := http.Get(baseURL + "/api/v1/series?tenant=bank-alpha")
	if err != nil {
		t.Fatal(err)
	}
	sBody, _ := io.ReadAll(sResp.Body)
	sResp.Body.Close()
	if sResp.StatusCode != http.StatusOK || !strings.Contains(string(sBody), `"tenant": "bank-alpha"`) || strings.Contains(string(sBody), "fintech-beta") {
		t.Fatalf("unexpected /api/v1/series response: %s", string(sBody))
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

	// 3. Verify job status is completed and linked to ingested RunID
	jobResp, err := http.Get(baseURL + "/api/v1/jobs/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	jobBody, _ := io.ReadAll(jobResp.Body)
	jobResp.Body.Close()
	var completed Job
	if err := json.Unmarshal(jobBody, &completed); err != nil {
		t.Fatal(err)
	}
	if completed.Status != JobCompleted || completed.RunID == "" || completed.Verdict != checker.Linearizable {
		t.Fatalf("expected completed linearizable job with RunID, got %+v", completed)
	}

	// 4. Verify GET /api/v1/trends returns the ingested run with resilience metrics
	trResp, err := http.Get(baseURL + "/api/v1/trends?target=raft")
	if err != nil {
		t.Fatal(err)
	}
	trBody, _ := io.ReadAll(trResp.Body)
	trResp.Body.Close()
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
}
