package server

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	"github.com/kemalhafizh/strife/pkg/model"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/report"
)

const (
	writeTok = "write-token-0123456789"
	readTok  = "read-token-0123456789"
)

// liveReport builds a real, checked report: a write of 1 then a read that
// returns 1 (linearizable) or 2 (a violation).
func liveReport(t *testing.T, target string, violate bool, seed uint64) (*report.Report, []byte, string) {
	t.Helper()
	read := int64(1)
	if violate {
		read = 2
	}
	ops := []history.Op{
		{Process: 0, Type: history.Invoke, F: history.FWrite, Key: "k", Value: history.Int(1), Time: 10},
		{Process: 0, Type: history.OK, F: history.FWrite, Key: "k", Value: history.Int(1), Time: 20},
		{Process: 1, Type: history.Invoke, F: history.FRead, Key: "k", Time: 30},
		{Process: 1, Type: history.OK, F: history.FRead, Key: "k", Value: history.Int(read), Time: 40},
	}
	for i := range ops {
		ops[i].Index = i
	}
	check, err := checker.Check(model.KV{}, ops, checker.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sched := nemesis.Schedule{Seed: seed, Nodes: 3, Duration: time.Second, Faults: []nemesis.Fault{{ID: 0, Kind: nemesis.Partition, Nodes: []int{0}, Start: 0, End: time.Second}}}
	rep, err := report.New(report.KindLive, target, seed, map[string]string{"name": target}, sched, ops, check, "")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(rep)
	var h bytes.Buffer
	if err := history.WriteJSONL(&h, ops); err != nil {
		t.Fatal(err)
	}
	return rep, data, h.String()
}

type harness struct {
	t    *testing.T
	srv  *Server
	http *httptest.Server
	priv ed25519.PrivateKey
	hook *hookSink
	dir  string
}

type hookSink struct {
	mu   sync.Mutex
	msgs []string
}

func newHarness(t *testing.T, tweak func(*Config)) *harness {
	t.Helper()
	dir := t.TempDir()
	pub, priv, err := attest.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := attest.SaveKeys(filepath.Join(dir, "ci.key"), filepath.Join(dir, "ci.pub"), priv, pub); err != nil {
		t.Fatal(err)
	}
	sink := &hookSink{}
	hookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sink.mu.Lock()
		sink.msgs = append(sink.msgs, string(body))
		sink.mu.Unlock()
	}))
	t.Cleanup(hookSrv.Close)
	t.Setenv("TEST_WRITE_TOKEN", writeTok)
	cfg := &Config{
		Data: filepath.Join(dir, "data"),
		Projects: []ProjectConfig{{
			Name: "payments", Token: "env:TEST_WRITE_TOKEN", ReadToken: readTok,
			TrustedKeys: []string{filepath.Join(dir, "ci.pub")}, Webhook: hookSrv.URL,
		}},
	}
	if tweak != nil {
		tweak(cfg)
	}
	h := &harness{t: t, priv: priv, hook: sink, dir: dir}
	h.start(cfg)
	return h
}

func (h *harness) start(cfg *Config) {
	h.t.Helper()
	srv, err := New(cfg, Options{Logf: h.t.Logf})
	if err != nil {
		h.t.Fatal(err)
	}
	h.srv = srv
	h.http = httptest.NewServer(srv.Handler())
	h.t.Cleanup(h.http.Close)
}

func (h *harness) do(method, path, tok string, body any) (int, []byte) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, h.http.URL+path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (h *harness) push(u Upload) (int, PushResult, string) {
	h.t.Helper()
	code, body := h.do("POST", "/api/v1/projects/payments/runs", writeTok, u)
	var res PushResult
	_ = json.Unmarshal(body, &res)
	return code, res, string(body)
}

func (h *harness) signed(rep *report.Report) json.RawMessage {
	h.t.Helper()
	b, err := attest.Sign(rep, h.priv, "ci")
	if err != nil {
		h.t.Fatal(err)
	}
	data, _ := json.Marshal(b)
	return data
}

func TestPushVerifyAndRegressions(t *testing.T) {
	h := newHarness(t, nil)
	rep, data, hist := liveReport(t, "etcd-3", false, 1)

	code, res, body := h.push(Upload{Report: data, History: hist, Bundle: h.signed(rep), Commit: "abc123", Branch: "main"})
	if code != http.StatusCreated || !res.Created || res.Run.Change != ChangeNew || !res.Run.Attested || !res.Run.HistoryVerified {
		t.Fatalf("first push: %d %s", code, body)
	}
	if !strings.Contains(res.URL, "/ui/payments/runs/"+rep.Fingerprint) {
		t.Fatalf("url %q", res.URL)
	}
	// Pushing the same run again is idempotent.
	if code, res, _ := h.push(Upload{Report: data}); code != http.StatusOK || res.Created {
		t.Fatalf("duplicate push: %d %+v", code, res)
	}

	// The same series now fails: a regression, and the webhook hears it.
	bad, badData, badHist := liveReport(t, "etcd-3", true, 2)
	code, res, body = h.push(Upload{Report: badData, History: badHist, Bundle: h.signed(bad), Commit: "def456"})
	if code != http.StatusCreated || res.Run.Change != ChangeRegression || res.Run.Previous != rep.Fingerprint {
		t.Fatalf("regression push: %d %s", code, body)
	}
	// And then passes again: fixed.
	good, goodData, _ := liveReport(t, "etcd-3", false, 3)
	if _, res, body = h.push(Upload{Report: goodData, Bundle: h.signed(good)}); res.Run.Change != ChangeFixed {
		t.Fatalf("fix push: %s", body)
	}
	h.srv.Wait()
	h.hook.mu.Lock()
	msgs := strings.Join(h.hook.msgs, "\n")
	h.hook.mu.Unlock()
	if len(h.hook.msgs) != 2 || !strings.Contains(msgs, "etcd-3: regressed (violation) at def456") || !strings.Contains(msgs, "is fixed") {
		t.Fatalf("webhooks: %v", h.hook.msgs)
	}

	// Reading: the read token works, the series shows the history.
	code, body2 := h.do("GET", "/api/v1/projects/payments/series", readTok, nil)
	var series []Series
	_ = json.Unmarshal(body2, &series)
	if code != 200 || len(series) != 1 || series[0].Runs != 3 || series[0].Latest.ID != good.Fingerprint ||
		len(series[0].Recent) != 3 || series[0].Recent[1] != checker.Violation {
		t.Fatalf("series: %d %s", code, body2)
	}
	code, body2 = h.do("GET", "/api/v1/projects/payments/runs?change=regression", readTok, nil)
	var runs []Run
	_ = json.Unmarshal(body2, &runs)
	if code != 200 || len(runs) != 1 || runs[0].ID != bad.Fingerprint {
		t.Fatalf("filtered runs: %s", body2)
	}
	if code, body2 = h.do("GET", "/api/v1/projects/payments/runs/"+bad.Fingerprint+"/files/history.jsonl", readTok, nil); code != 200 || string(body2) != badHist {
		t.Fatalf("history file: %d", code)
	}
	if code, body2 = h.do("GET", "/api/v1/projects/payments/runs/"+bad.Fingerprint+"/report.html", readTok, nil); code != 200 || !bytes.Contains(body2, []byte("<html")) {
		t.Fatalf("report html: %d", code)
	}
	// An on-demand dossier re-verifies the signature.
	code, body2 = h.do("GET", "/api/v1/projects/payments/runs/"+rep.Fingerprint+"/dossier?frameworks=dora,soc2", readTok, nil)
	if code != 200 || !strings.Contains(string(body2), `"attested": true`) || !strings.Contains(string(body2), "dora.resilience-testing") {
		t.Fatalf("dossier: %d %s", code, body2)
	}
	// The dashboard renders and escapes.
	if code, body2 = h.do("GET", "/ui/payments", readTok, nil); code != 200 || !bytes.Contains(body2, []byte("etcd-3")) {
		t.Fatalf("dashboard: %d", code)
	}
	if code, body2 = h.do("GET", "/ui/payments/runs/"+bad.Fingerprint, readTok, nil); code != 200 || !bytes.Contains(body2, []byte("regression")) {
		t.Fatalf("run page: %d", code)
	}

	// Everything survives a restart: the index is rebuilt from disk.
	h.http.Close()
	h.start(h.srv.cfg)
	if code, body2 = h.do("GET", "/api/v1/projects/payments/runs", readTok, nil); code != 200 || strings.Count(string(body2), `"id"`) != 3 {
		t.Fatalf("after restart: %s", body2)
	}
}

func TestPushRefusesWhatDoesNotVerify(t *testing.T) {
	h := newHarness(t, nil)
	rep, data, hist := liveReport(t, "pg", false, 1)

	// Edited report: the fingerprint no longer matches.
	edited := bytes.Replace(data, []byte(`"target":"pg"`), []byte(`"target":"pg-prod"`), 1)
	if code, _, body := h.push(Upload{Report: edited}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "fingerprint") {
		t.Fatalf("edited report: %d %s", code, body)
	}
	// History that is not the report's.
	_, _, other := liveReport(t, "pg", true, 1)
	if code, _, body := h.push(Upload{Report: data, History: other}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "history") {
		t.Fatalf("foreign history: %d %s", code, body)
	}
	_ = hist
	// A signature by an untrusted key.
	_, stranger, _ := ed25519.GenerateKey(nil)
	b, _ := attest.Sign(rep, stranger, "mallory")
	bad, _ := json.Marshal(b)
	if code, _, body := h.push(Upload{Report: data, Bundle: bad}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "trust") {
		t.Fatalf("untrusted signature: %d %s", code, body)
	}
	// A valid signature about another report.
	other2, _, _ := liveReport(t, "pg", true, 9)
	if code, _, body := h.push(Upload{Report: data, Bundle: h.signed(other2)}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "different report") {
		t.Fatalf("mismatched signature: %d %s", code, body)
	}
	// Tokens: no token, the read token, another project.
	if code, _ := h.do("POST", "/api/v1/projects/payments/runs", "", Upload{Report: data}); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := h.do("POST", "/api/v1/projects/payments/runs", readTok, Upload{Report: data}); code != http.StatusUnauthorized {
		t.Fatalf("read token pushed: %d", code)
	}
	if code, _ := h.do("GET", "/api/v1/projects/other/runs", writeTok, nil); code != http.StatusUnauthorized {
		t.Fatalf("unknown project: %d", code)
	}
	if code, _ := h.do("GET", "/api/v1/projects/payments/runs", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous read: %d", code)
	}
	if code, _ := h.do("GET", "/api/v1/projects/payments/runs/../../../etc/passwd", readTok, nil); code == http.StatusOK {
		t.Fatal("path traversal answered")
	}
	// Nothing above was stored.
	if _, body := h.do("GET", "/api/v1/projects/payments/runs", readTok, nil); string(bytes.TrimSpace(body)) != "[]" {
		t.Fatalf("rejected runs stored: %s", body)
	}
}

func TestRequireSignatureAndRetention(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.Projects[0].RequireSignature = true
		c.Projects[0].Retain = 2
	})
	rep, data, _ := liveReport(t, "kafka", false, 1)
	if code, _, body := h.push(Upload{Report: data}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "requires") {
		t.Fatalf("unsigned push accepted: %d %s", code, body)
	}
	var ids []string
	for seed := uint64(1); seed <= 3; seed++ {
		r, d, _ := liveReport(t, "kafka", false, seed)
		if code, _, body := h.push(Upload{Report: d, Bundle: h.signed(r)}); code != http.StatusCreated {
			t.Fatalf("seed %d: %s", seed, body)
		}
		ids = append(ids, r.Fingerprint)
	}
	_ = rep
	_, body := h.do("GET", "/api/v1/projects/payments/runs", readTok, nil)
	var kept []Run
	_ = json.Unmarshal(body, &kept)
	if len(kept) != 2 || kept[0].ID != ids[2] || kept[1].ID != ids[1] {
		t.Fatalf("retention kept the wrong runs: %s", body)
	}
	if _, err := os.Stat(filepath.Join(h.srv.cfg.Data, "projects", "payments", "runs", ids[0])); !os.IsNotExist(err) {
		t.Fatalf("evicted run still on disk: %v", err)
	}
}

func campaignManifest(t *testing.T, verdicts ...checker.Verdict) json.RawMessage {
	t.Helper()
	m := &campaign.Manifest{Tool: "strife", CampaignID: "c", System: "raft", Faults: []nemesis.Kind{nemesis.Partition}, TotalSeeds: len(verdicts)}
	for i, v := range verdicts {
		m.Seeds = append(m.Seeds, campaign.SeedRecord{Seed: uint64(i + 1), Verdict: v, TraceHash: strings.Repeat("a", 64)})
		switch v {
		case checker.Linearizable:
			m.Passed++
		case checker.Violation:
			m.Violations++
		}
	}
	m.Fingerprint = m.ComputeFingerprint()
	data, _ := json.Marshal(m)
	return data
}

func TestCampaignRegressionPerSeed(t *testing.T) {
	h := newHarness(t, nil)
	if code, res, body := h.push(Upload{Campaign: campaignManifest(t, checker.Linearizable, checker.Linearizable)}); code != http.StatusCreated || res.Run.Change != ChangeNew {
		t.Fatalf("baseline: %s", body)
	}
	code, res, body := h.push(Upload{Campaign: campaignManifest(t, checker.Linearizable, checker.Violation)})
	if code != http.StatusCreated || res.Run.Change != ChangeRegression || res.Run.Diff == nil || len(res.Run.Diff.Regressed) != 1 || res.Run.Diff.Regressed[0].Seed != 2 {
		t.Fatalf("campaign regression: %s", body)
	}
	// Campaign manifests cannot carry a history: the seeds' reports do.
	if code, _, _ := h.push(Upload{Campaign: campaignManifest(t, checker.Linearizable), History: "x"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("campaign with history: %d", code)
	}
	if code, body := h.do("GET", "/ui/payments/runs/"+res.Run.ID, readTok, nil); code != 200 || !bytes.Contains(body, []byte("regressed")) {
		t.Fatalf("campaign page: %d", code)
	}
	if code, body := h.do("GET", "/api/v1/projects/payments/runs/"+res.Run.ID+"/dossier?format=md", readTok, nil); code != 200 || !bytes.Contains(body, []byte("Dossier")) {
		t.Fatalf("campaign dossier: %d %s", code, body)
	}
}

func TestConfigValidation(t *testing.T) {
	for name, c := range map[string]Config{
		"no projects":  {},
		"bad name":     {Projects: []ProjectConfig{{Name: "Bad Name", Token: strings.Repeat("x", 20)}}},
		"short token":  {Projects: []ProjectConfig{{Name: "a", Token: "short"}}},
		"no token":     {Projects: []ProjectConfig{{Name: "a"}}},
		"sig, no keys": {Projects: []ProjectConfig{{Name: "a", Token: strings.Repeat("x", 20), RequireSignature: true}}},
		"twice":        {Projects: []ProjectConfig{{Name: "a", Token: strings.Repeat("x", 20)}, {Name: "a", Token: strings.Repeat("y", 20)}}},
	} {
		c.Data = t.TempDir()
		if _, err := New(&c, Options{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func (h *harness) signedCampaign(manifest json.RawMessage) json.RawMessage {
	h.t.Helper()
	var m campaign.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		h.t.Fatal(err)
	}
	b, err := attest.SignCampaign(m.Predicate("ci"), h.priv)
	if err != nil {
		h.t.Fatal(err)
	}
	data, _ := json.Marshal(b)
	return data
}

// A project that requires signatures takes campaigns signed by a trusted
// key, and only with the signature over that very campaign; the dossier
// generated from it carries the signature.
func TestSignedCampaigns(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Projects[0].RequireSignature = true })
	pass := campaignManifest(t, checker.Linearizable, checker.Linearizable)
	other := campaignManifest(t, checker.Linearizable, checker.Violation)
	if code, _, body := h.push(Upload{Campaign: pass}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "strife sign --campaign") {
		t.Fatalf("unsigned campaign accepted: %d %s", code, body)
	}
	if code, _, body := h.push(Upload{Campaign: pass, Bundle: h.signedCampaign(other)}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "not") {
		t.Fatalf("another campaign's signature accepted: %d %s", code, body)
	}
	rep, _, _ := liveReport(t, "kafka", false, 1)
	if code, _, body := h.push(Upload{Campaign: pass, Bundle: h.signed(rep)}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "report") {
		t.Fatalf("a report's signature accepted for a campaign: %d %s", code, body)
	}
	_, foreign, _ := attest.GenerateKeyPair()
	var m campaign.Manifest
	_ = json.Unmarshal(pass, &m)
	fb, _ := attest.SignCampaign(m.Predicate("mallory"), foreign)
	fdata, _ := json.Marshal(fb)
	if code, _, body := h.push(Upload{Campaign: pass, Bundle: fdata}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "not signed by any key this project trusts") {
		t.Fatalf("untrusted key accepted: %d %s", code, body)
	}
	code, res, body := h.push(Upload{Campaign: pass, Bundle: h.signedCampaign(pass)})
	if code != http.StatusCreated || !res.Run.Attested || res.Run.Signer != "ci" {
		t.Fatalf("signed campaign: %d %s", code, body)
	}
	code, out := h.do("GET", "/api/v1/projects/payments/runs/"+res.Run.ID+"/dossier", readTok, nil)
	var d struct {
		Summary struct {
			Attested bool   `json:"attested"`
			Signer   string `json:"signer"`
		} `json:"summary"`
	}
	if code != 200 || json.Unmarshal(out, &d) != nil || !d.Summary.Attested || d.Summary.Signer != "ci" {
		t.Fatalf("campaign dossier is not attested: %d %s", code, out)
	}
}
