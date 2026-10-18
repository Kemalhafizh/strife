package bridge

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kemalhafizh/strife/pkg/attest"
	"github.com/kemalhafizh/strife/pkg/explore"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/report"
)

// The README's walkthrough: raft-unsafe-fsync, seed 13, kill and io-stall.
var readme = SimRequest{System: "raft-unsafe-fsync", Faults: "kill,io-stall", Seed: 13}

func TestSimulateMatchesTheEngine(t *testing.T) {
	got, err := Simulate(readme)
	if err != nil {
		t.Fatal(err)
	}
	cfg := explore.DefaultConfig()
	cfg.System, cfg.Seed = readme.System, readme.Seed
	cfg.Faults, _ = nemesis.ParseKinds(readme.Faults)
	want, err := explore.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.TraceHash != want.TraceHash || got.HistoryHash != want.HistoryHash || got.ScheduleHash != want.ScheduleHash {
		t.Fatalf("hashes differ from explore.Run: got %s/%s, want %s/%s", got.TraceHash, got.HistoryHash, want.TraceHash, want.HistoryHash)
	}
	if got.Verdict != "violation" || len(got.Failures) == 0 || got.Failures[0].Summary == "" {
		t.Fatalf("verdict %s, failures %+v: want an explained violation", got.Verdict, got.Failures)
	}
	if got.Ops.Invocations == 0 || len(got.Faults) == 0 || got.Events != want.Events {
		t.Fatalf("summary incomplete: %+v", got)
	}
	if got.Command != "strife sim --system raft-unsafe-fsync --faults kill,io-stall --seed 13" {
		t.Fatalf("command %q", got.Command)
	}
	if got.ReportHTML != "" {
		t.Fatal("report rendered without being asked for")
	}
}

func TestSimulateRendersTheCLIReport(t *testing.T) {
	req := SimRequest{System: "raft-stale-reads", Faults: "partition", Seed: 2, Report: true}
	got, err := Simulate(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.ReportHTML, "<html") || !strings.Contains(got.ReportHTML, got.Command) {
		t.Fatalf("report.html missing or without the reproduction command (%d bytes)", len(got.ReportHTML))
	}
}

func TestSimulateCommandAndDefaults(t *testing.T) {
	cases := []struct {
		req  SimRequest
		want string
	}{
		{SimRequest{Seed: 7}, "strife sim --system raft --seed 7"},
		{SimRequest{System: "raft", Faults: "none", Seed: 1}, "strife sim --system raft --faults none --seed 1"},
		{SimRequest{System: "raft-lease", Faults: "clock-skew,partition", Seed: 2, Nodes: 3, Clients: 4, Duration: "5s"},
			"strife sim --system raft-lease --faults clock-skew,partition --seed 2 --nodes 3 --clients 4 --duration 5s"},
	}
	for _, c := range cases {
		cfg, err := c.req.config()
		if err != nil {
			t.Fatal(err)
		}
		if got := command(cfg); got != c.want {
			t.Errorf("command(%+v) = %q, want %q", c.req, got, c.want)
		}
	}
}

func TestSimulateRejectsWhatTheBrowserCannotRun(t *testing.T) {
	for _, req := range []SimRequest{
		{System: "nope", Seed: 1},
		{Faults: "earthquake", Seed: 1},
		{Duration: "2m", Seed: 1},
		{Duration: "soon", Seed: 1},
		{Nodes: 20, Seed: 1},
		{Clients: 400, Seed: 1},
	} {
		if _, err := Simulate(req); err == nil {
			t.Errorf("Simulate(%+v) succeeded", req)
		}
	}
}

func readmeHistory(t *testing.T) []byte {
	t.Helper()
	cfg := explore.DefaultConfig()
	cfg.System, cfg.Seed = "raft-stale-reads", 2
	cfg.Faults, _ = nemesis.ParseKinds("partition")
	res, err := explore.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := history.WriteJSONL(&buf, res.History); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestCheckAgreesWithTheSimulation(t *testing.T) {
	jsonl := readmeHistory(t)
	got, err := Check(jsonl, CheckRequest{Name: "ops.jsonl", Report: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "kv" || got.Verdict != "violation" || len(got.Failures) == 0 {
		t.Fatalf("got %s/%s with %d failures, want a kv violation", got.Model, got.Verdict, len(got.Failures))
	}
	ops, _ := history.ReadJSONL(bytes.NewReader(jsonl))
	if got.HistoryHash != history.Hash(ops) || got.Fingerprint == "" {
		t.Fatalf("history hash %s, fingerprint %q", got.HistoryHash, got.Fingerprint)
	}
	if !strings.Contains(got.ReportHTML, "ops.jsonl") {
		t.Fatal("report.html does not name the checked file")
	}
}

func TestCheckRejectsBadInput(t *testing.T) {
	if _, err := Check([]byte("{not json\n"), CheckRequest{}); err == nil {
		t.Error("garbage history accepted")
	}
	if _, err := Check(readmeHistory(t), CheckRequest{Model: "graph"}); err == nil {
		t.Error("unknown model accepted")
	}
}

func signedRun(t *testing.T) (bundle, pubPEM, jsonl []byte, priv ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := attest.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(pub)
	pubPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})

	jsonl = readmeHistory(t)
	ops, _ := history.ReadJSONL(bytes.NewReader(jsonl))
	sim, err := Simulate(SimRequest{System: "raft-stale-reads", Faults: "partition", Seed: 2})
	if err != nil {
		t.Fatal(err)
	}
	r := &report.Report{Tool: "strife", Kind: report.KindSimulation, Target: sim.System, Seed: sim.Seed,
		Verdict: "violation", HistoryHash: history.Hash(ops), TraceHash: sim.TraceHash}
	b, err := attest.Sign(r, priv, "qa-team")
	if err != nil {
		t.Fatal(err)
	}
	bundle, _ = json.Marshal(b)
	return bundle, pubPEM, jsonl, priv
}

func TestVerifyAttestation(t *testing.T) {
	bundle, pubPEM, jsonl, _ := signedRun(t)

	ok, err := VerifyAttestation(bundle, pubPEM, jsonl)
	if err != nil {
		t.Fatal(err)
	}
	if !ok.Valid || !ok.HistoryChecked || !ok.HistoryMatches || ok.Signer != "qa-team" || ok.Verdict != "violation" || ok.Seed != 2 {
		t.Fatalf("valid bundle rejected: %+v", ok)
	}
	if len(ok.BundleKeys) != 1 || ok.BundleKeys[0] != ok.KeyID {
		t.Fatalf("key ids %v vs %s", ok.BundleKeys, ok.KeyID)
	}

	// Raw key bytes are accepted too, as by the CLI.
	block, _ := pem.Decode(pubPEM)
	k, _ := x509.ParsePKIXPublicKey(block.Bytes)
	if raw, err := VerifyAttestation(bundle, k.(ed25519.PublicKey), nil); err != nil || !raw.Valid || raw.HistoryChecked {
		t.Fatalf("raw key: %+v, %v", raw, err)
	}

	other, _, _ := ed25519.GenerateKey(nil)
	der, _ := x509.MarshalPKIXPublicKey(other)
	wrong, err := VerifyAttestation(bundle, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil)
	if err != nil || wrong.Valid || wrong.Problem == "" || wrong.KeyID == ok.KeyID {
		t.Fatalf("wrong key accepted: %+v, %v", wrong, err)
	}

	var b attest.Bundle
	_ = json.Unmarshal(bundle, &b)
	b.Statement.Predicate.Verdict = "linearizable" // the unsigned copy must not matter
	b.Payload = b.Payload[:len(b.Payload)-8] + "AAAAAAA="
	tampered, _ := json.Marshal(b)
	if res, err := VerifyAttestation(tampered, pubPEM, nil); err == nil && res.Valid {
		t.Fatal("tampered payload accepted")
	}

	edited := bytes.Replace(jsonl, []byte(`"ok"`), []byte(`"fail"`), 1)
	res, err := VerifyAttestation(bundle, pubPEM, edited)
	if err != nil || res.Valid || !res.HistoryChecked || res.HistoryMatches {
		t.Fatalf("edited history accepted: %+v, %v", res, err)
	}

	if _, err := VerifyAttestation([]byte("{}"), []byte("not a key"), nil); err == nil {
		t.Fatal("bad key accepted")
	}
	if _, err := VerifyAttestation([]byte("nope"), pubPEM, nil); err == nil {
		t.Fatal("bad bundle accepted")
	}
}

func TestVerifyRelease(t *testing.T) {
	_, pubPEM, _, priv := signedRun(t)
	dir := t.TempDir()
	files := map[string][]byte{
		"strife_v9.9.9_linux_amd64.tar.gz": []byte("linux archive"),
		"strife_v9.9.9_windows_arm64.zip":  []byte("windows archive"),
		"SHA256SUMS":                       []byte("sums"),
	}
	var paths []string
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	rb, err := attest.SignRelease(paths, attest.ReleasePredicate{Version: "v9.9.9", Commit: "abc123", Signer: "strife-release"}, priv)
	if err != nil {
		t.Fatal(err)
	}
	bundle, _ := json.Marshal(rb)

	// The released files themselves pass, and agree with the CLI's check.
	if _, err := attest.VerifyRelease(rb, priv.Public().(ed25519.PublicKey), dir); err != nil {
		t.Fatalf("CLI verification failed: %v", err)
	}
	got, err := VerifyRelease(bundle, pubPEM, []NamedFile{{Name: "strife_v9.9.9_linux_amd64.tar.gz", Data: files["strife_v9.9.9_linux_amd64.tar.gz"]}})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Version != "v9.9.9" || got.Commit != "abc123" || len(got.Subjects) != 3 || got.Files[0].Status != "match" {
		t.Fatalf("genuine archive rejected: %+v", got)
	}

	got, err = VerifyRelease(bundle, pubPEM, []NamedFile{
		{Name: "strife_v9.9.9_linux_amd64.tar.gz", Data: []byte("linux archive, patched")},
		{Name: "strife_v9.9.9_darwin_arm64.tar.gz", Data: []byte("?")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Valid || got.Files[0].Status != "mismatch" || got.Files[1].Status != "not-in-release" {
		t.Fatalf("altered or foreign files accepted: %+v", got)
	}

	other, _, _ := ed25519.GenerateKey(nil)
	der, _ := x509.MarshalPKIXPublicKey(other)
	got, err = VerifyRelease(bundle, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil)
	if err != nil || got.Valid || got.Problem == "" {
		t.Fatalf("wrong key accepted: %+v, %v", got, err)
	}

	forged, _ := json.Marshal(attest.ReleaseBundle{Payload: "e30=", Signatures: rb.Signatures})
	if got, err := VerifyRelease(forged, pubPEM, nil); err != nil || got.Valid || got.Problem == "" {
		t.Fatalf("a signature over another payload passed: %+v, %v", got, err)
	}
}

func TestListings(t *testing.T) {
	if len(Systems()) < 4 || len(FaultKinds()) != len(nemesis.AllKinds) || len(Models()) == 0 {
		t.Fatalf("systems %d, faults %d, models %d", len(Systems()), len(FaultKinds()), len(Models()))
	}
}
