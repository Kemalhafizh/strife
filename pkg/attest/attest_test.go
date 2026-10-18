package attest

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/report"
)

func sampleTestReport() *report.Report {
	v := int64(42)
	ops := []history.Op{
		{Process: 0, Type: history.Invoke, F: history.FWrite, Key: "k0", Value: &v, Time: 1000, Index: 0},
		{Process: 0, Type: history.OK, F: history.FWrite, Key: "k0", Value: &v, Time: 2000, Index: 1},
	}
	r, _ := report.New(
		report.KindSimulation,
		"raft-stale-reads",
		13,
		map[string]any{"nodes": 5},
		nemesis.Schedule{Nodes: 5},
		ops,
		checker.Report{
			Model:   "kv",
			Verdict: checker.Violation,
		},
		"trace-hash-abc",
	)
	return r
}

func TestKeyPairGenerationAndPEM(t *testing.T) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	dir := t.TempDir()
	privPath := filepath.Join(dir, "test.key")
	pubPath := filepath.Join(dir, "test.pub")

	if err := SaveKeys(privPath, pubPath, priv, pub); err != nil {
		t.Fatalf("SaveKeys failed: %v", err)
	}

	loadedPriv, err := LoadPrivateKey(privPath)
	if err != nil {
		t.Fatalf("LoadPrivateKey failed: %v", err)
	}

	loadedPub, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatalf("LoadPublicKey failed: %v", err)
	}

	if !pub.Equal(loadedPub) {
		t.Errorf("loaded public key does not match original")
	}
	if !priv.Equal(loadedPriv) {
		t.Errorf("loaded private key does not match original")
	}

	// The same key parses from bytes: PEM as written, and raw.
	pemBytes, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"pem": pemBytes, "raw": []byte(pub)} {
		if got, err := ParsePublicKey(data); err != nil || !pub.Equal(got) {
			t.Errorf("ParsePublicKey(%s): %v", name, err)
		}
	}
	if _, err := ParsePublicKey([]byte("not a key")); err == nil {
		t.Error("ParsePublicKey accepted garbage")
	}
	if err := os.WriteFile(pubPath, []byte("not a key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPublicKey(pubPath); err == nil || !strings.Contains(err.Error(), pubPath) {
		t.Errorf("LoadPublicKey error should name the file: %v", err)
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	rep := sampleTestReport()
	bundle, err := Sign(rep, priv, "bank-auditor-key-1")
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	if bundle.PayloadType != PayloadTypeJSON {
		t.Errorf("expected payload type %s, got %s", PayloadTypeJSON, bundle.PayloadType)
	}
	if len(bundle.Signatures) != 1 {
		t.Fatalf("expected 1 signature, got %d", len(bundle.Signatures))
	}
	if bundle.Signatures[0].KeyID != KeyID(pub) {
		t.Errorf("expected key ID %s, got %s", KeyID(pub), bundle.Signatures[0].KeyID)
	}

	res, err := Verify(bundle, pub)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !res.Valid {
		t.Errorf("expected verification valid")
	}
	if res.Target != "raft-stale-reads" {
		t.Errorf("expected target 'raft-stale-reads', got %q", res.Target)
	}
	if res.Seed != 13 {
		t.Errorf("expected seed 13, got %d", res.Seed)
	}
	if res.Verdict != checker.Violation {
		t.Errorf("expected verdict 'violation', got %q", res.Verdict)
	}
	if res.Fingerprint != rep.Fingerprint {
		t.Errorf("expected fingerprint %s, got %s", rep.Fingerprint, res.Fingerprint)
	}
	if res.Signer != "bank-auditor-key-1" {
		t.Errorf("expected signer 'bank-auditor-key-1', got %q", res.Signer)
	}
}

func TestTamperedPayloadFails(t *testing.T) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	rep := sampleTestReport()
	bundle, err := Sign(rep, priv, "auditor")
	if err != nil {
		t.Fatal(err)
	}

	// Tamper: decode, flip bytes in JSON, re-encode base64
	payloadBytes, _ := base64.StdEncoding.DecodeString(bundle.Payload)
	var stmt Statement
	_ = json.Unmarshal(payloadBytes, &stmt)
	stmt.Predicate.Verdict = checker.Linearizable // Falsify verdict from violation to linearizable
	tamperedBytes, _ := json.Marshal(stmt)
	bundle.Payload = base64.StdEncoding.EncodeToString(tamperedBytes)

	// Verification must detect the tampering and fail
	_, err = Verify(bundle, pub)
	if err == nil {
		t.Fatalf("expected verification to fail on tampered statement payload")
	}
}

func TestWrongPublicKeyFails(t *testing.T) {
	_, priv, _ := GenerateKeyPair()
	otherPub, _, _ := GenerateKeyPair()

	rep := sampleTestReport()
	bundle, err := Sign(rep, priv, "auditor")
	if err != nil {
		t.Fatal(err)
	}

	_, err = Verify(bundle, otherPub)
	if err == nil {
		t.Fatalf("expected verification to fail when checked against wrong public key")
	}
}

func TestHistoryFileVerification(t *testing.T) {
	pub, priv, _ := GenerateKeyPair()

	v := int64(100)
	ops := []history.Op{
		{Process: 0, Type: history.Invoke, F: history.FWrite, Key: "x", Value: &v, Time: 10, Index: 0},
		{Process: 0, Type: history.OK, F: history.FWrite, Key: "x", Value: &v, Time: 20, Index: 1},
	}
	rep, _ := report.New(
		report.KindCheck,
		"kv-check",
		1,
		nil,
		nemesis.Schedule{},
		ops,
		checker.Report{Verdict: checker.Linearizable},
		"",
	)

	bundle, err := Sign(rep, priv, "sec-ops")
	if err != nil {
		t.Fatal(err)
	}

	res, err := Verify(bundle, pub)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	histPath := filepath.Join(dir, "history.jsonl")
	f, _ := os.Create(histPath)
	_ = history.WriteJSONL(f, ops)
	f.Close()

	// Matching history file must pass
	ok, err := VerifyHistoryFile(res, histPath)
	if err != nil || !ok || !res.HistoryVerified {
		t.Fatalf("expected history verification to pass: %v", err)
	}

	// Tampered history file must fail
	tamperedOps := append(ops, history.Op{Process: 1, Type: history.OK, F: history.FRead, Key: "x", Time: 30})
	f2, _ := os.Create(histPath)
	_ = history.WriteJSONL(f2, tamperedOps)
	f2.Close()

	ok, err = VerifyHistoryFile(res, histPath)
	if err == nil || ok {
		t.Fatalf("expected tampered history file to fail verification")
	}
}

func TestCampaignAttestation(t *testing.T) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	p := CampaignPredicate{System: "raft", Fingerprint: strings.Repeat("c", 64), Verdict: "linearizable", Seeds: 100, Ops: 4000, Signer: "ci"}
	b, err := SignCampaign(p, priv)
	if err != nil {
		t.Fatal(err)
	}
	stmt, res, err := VerifyCampaign(b, pub)
	if err != nil || !res.Valid || res.Kind != KindCampaign || res.Fingerprint != p.Fingerprint || res.Target != "raft" || stmt.Predicate.Seeds != 100 {
		t.Fatalf("round trip: %+v %+v %v", stmt, res, err)
	}
	data, _ := json.Marshal(b)
	if res, err := VerifyAny(data, pub); err != nil || res.Kind != KindCampaign {
		t.Fatalf("VerifyAny on a campaign bundle: %+v %v", res, err)
	}
	// Another key, or a payload changed after signing, does not verify.
	other, _, _ := GenerateKeyPair()
	if _, err := VerifyAny(data, other); !errors.Is(err, ErrSignature) {
		t.Fatalf("other key: %v", err)
	}
	p.Verdict = "violation"
	forged, _ := SignCampaign(p, priv)
	tampered := *b
	tampered.Payload = forged.Payload
	if _, _, err := VerifyCampaign(&tampered, pub); !errors.Is(err, ErrSignature) {
		t.Fatalf("payload swapped under the signature: %v", err)
	}
	// A report's verifier refuses a campaign's statement.
	if _, err := Verify(&Bundle{PayloadType: b.PayloadType, Payload: b.Payload, Signatures: b.Signatures}, pub); err == nil {
		t.Fatal("Verify accepted a campaign statement as a report's")
	}
	if _, err := SignCampaign(CampaignPredicate{System: "raft"}, priv); err == nil {
		t.Fatal("signed a campaign without a fingerprint")
	}
}

func TestParsePrivateKey(t *testing.T) {
	pub, priv, _ := GenerateKeyPair()
	dir := t.TempDir()
	if err := SaveKeys(filepath.Join(dir, "k.key"), filepath.Join(dir, "k.pub"), priv, pub); err != nil {
		t.Fatal(err)
	}
	pemBytes, _ := os.ReadFile(filepath.Join(dir, "k.key"))
	for name, data := range map[string][]byte{"pem": pemBytes, "pem without newline": []byte(strings.TrimSpace(string(pemBytes))), "raw": priv} {
		if got, err := ParsePrivateKey(data); err != nil || !priv.Equal(got) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParsePrivateKey([]byte("nope")); err == nil {
		t.Error("garbage accepted")
	}
}
