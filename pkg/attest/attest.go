package attest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/report"
)

const (
	StatementTypeV01 = "https://in-toto.io/Statement/v0.1"
	PredicateTypeV1  = "https://strife.dev/attestation/v1"
	PayloadTypeJSON  = "application/vnd.in-toto+json"
)

// ResourceDescriptor describes an artifact covered by the statement.
type ResourceDescriptor struct {
	Name   string            `json:"name"`
	URI    string            `json:"uri,omitempty"`
	Digest map[string]string `json:"digest"`
}

// Predicate holds the Strife compliance verdict, audit metrics, and cryptographic hashes.
type Predicate struct {
	Tool         string          `json:"tool"`
	Version      string          `json:"version"`
	Kind         string          `json:"kind"`
	Target       string          `json:"target"`
	Seed         uint64          `json:"seed"`
	Verdict      checker.Verdict `json:"verdict"`
	Fingerprint  string          `json:"fingerprint"`
	ScheduleHash string          `json:"schedule_hash,omitempty"`
	HistoryHash  string          `json:"history_hash"`
	TraceHash    string          `json:"trace_hash,omitempty"`
	Events       uint64          `json:"events,omitempty"`
	SignedAt     string          `json:"signed_at"`
	Signer       string          `json:"signer"`
}

// Statement is an in-toto v0.1 attestation statement.
type Statement struct {
	Type          string               `json:"_type"`
	Subject       []ResourceDescriptor `json:"subject"`
	PredicateType string               `json:"predicateType"`
	Predicate     Predicate            `json:"predicate"`
}

// Signature wraps the cryptographic signature over the statement.
type Signature struct {
	KeyID string `json:"key_id"`
	Sig   string `json:"sig"` // Base64-encoded
}

// Bundle is the complete signed attestation artifact.
type Bundle struct {
	PayloadType string      `json:"payload_type"`
	Payload     string      `json:"payload"` // Base64-encoded canonical JSON of Statement
	Signatures  []Signature `json:"signatures"`
	Statement   *Statement  `json:"statement,omitempty"`
}

// KeyID computes the SHA-256 fingerprint of an Ed25519 public key.
func KeyID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:16])
}

// GenerateKeyPair generates a new Ed25519 keypair.
func GenerateKeyPair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("attest: failed to generate ed25519 key: %w", err)
	}
	return pub, priv, nil
}

// SaveKeys saves public and private keys to disk in standard PEM PKCS#8 format.
func SaveKeys(privPath, pubPath string, priv ed25519.PrivateKey, pub ed25519.PublicKey) error {
	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("attest: marshal private key: %w", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privBytes,
	})
	if err := os.WriteFile(privPath, privPEM, 0o600); err != nil {
		return fmt.Errorf("attest: write private key: %w", err)
	}

	pubBytes, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return fmt.Errorf("attest: marshal public key: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})
	if err := os.WriteFile(pubPath, pubPEM, 0o644); err != nil {
		return fmt.Errorf("attest: write public key: %w", err)
	}
	return nil
}

// LoadPrivateKey loads an Ed25519 private key from PEM or raw binary.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	priv, err := ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", err, path)
	}
	return priv, nil
}

// ParsePrivateKey reads an Ed25519 private key in the forms LoadPrivateKey
// accepts: PEM (PKCS #8, as written by SaveKeys) or the raw 64 bytes. CI
// jobs can then take the key from a secret instead of a file.
func ParsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	if block, _ := pem.Decode(data); block != nil {
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("attest: parse pkcs8 private key: %w", err)
		}
		if edPriv, ok := k.(ed25519.PrivateKey); ok {
			return edPriv, nil
		}
		return nil, fmt.Errorf("attest: key is not ed25519")
	}
	if len(data) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(data), nil
	}
	return nil, fmt.Errorf("attest: invalid private key format")
}

// LoadPublicKey loads an Ed25519 public key from PEM or raw binary.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pub, err := ParsePublicKey(data)
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", err, path)
	}
	return pub, nil
}

// ParsePublicKey reads an Ed25519 public key in the forms LoadPublicKey
// accepts: PEM (PKIX, as written by SaveKeys) or the raw 32 bytes. It needs
// no filesystem, so a browser build can verify bundles with it.
func ParsePublicKey(data []byte) (ed25519.PublicKey, error) {
	if block, _ := pem.Decode(data); block != nil {
		k, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("attest: parse pkix public key: %w", err)
		}
		if edPub, ok := k.(ed25519.PublicKey); ok {
			return edPub, nil
		}
		return nil, fmt.Errorf("attest: public key is not ed25519")
	}
	if len(data) == ed25519.PublicKeySize {
		return ed25519.PublicKey(data), nil
	}
	return nil, fmt.Errorf("attest: invalid public key format")
}

// NewStatement creates an in-toto v0.1 Statement from a Strife Report.
func NewStatement(r *report.Report, signer string) (*Statement, error) {
	if r.Fingerprint == "" {
		r.Fingerprint = report.Fingerprint(r)
	}

	subjects := []ResourceDescriptor{
		{
			Name: r.Target,
			Digest: map[string]string{
				"history_sha256": r.HistoryHash,
			},
		},
	}
	if r.ScheduleHash != "" {
		subjects = append(subjects, ResourceDescriptor{
			Name: "schedule",
			Digest: map[string]string{
				"sha256": r.ScheduleHash,
			},
		})
	}
	if r.TraceHash != "" {
		subjects = append(subjects, ResourceDescriptor{
			Name: "trace",
			Digest: map[string]string{
				"sha256": r.TraceHash,
			},
		})
	}

	if signer == "" {
		signer = "strife-auditor"
	}

	stmt := &Statement{
		Type:          StatementTypeV01,
		Subject:       subjects,
		PredicateType: PredicateTypeV1,
		Predicate: Predicate{
			Tool:         r.Tool,
			Version:      r.Version,
			Kind:         r.Kind,
			Target:       r.Target,
			Seed:         r.Seed,
			Verdict:      r.Verdict,
			Fingerprint:  r.Fingerprint,
			ScheduleHash: r.ScheduleHash,
			HistoryHash:  r.HistoryHash,
			TraceHash:    r.TraceHash,
			Events:       r.Events,
			SignedAt:     time.Now().UTC().Format(time.RFC3339),
			Signer:       signer,
		},
	}
	return stmt, nil
}

// Sign creates an in-toto attestation Statement and signs it with an Ed25519 private key.
func Sign(r *report.Report, priv ed25519.PrivateKey, signer string) (*Bundle, error) {
	stmt, err := NewStatement(r, signer)
	if err != nil {
		return nil, err
	}

	canonicalBytes, err := json.Marshal(stmt)
	if err != nil {
		return nil, fmt.Errorf("attest: marshal statement: %w", err)
	}

	sigBytes := ed25519.Sign(priv, canonicalBytes)
	pub := priv.Public().(ed25519.PublicKey)
	keyID := KeyID(pub)

	bundle := &Bundle{
		PayloadType: PayloadTypeJSON,
		Payload:     base64.StdEncoding.EncodeToString(canonicalBytes),
		Signatures: []Signature{
			{
				KeyID: keyID,
				Sig:   base64.StdEncoding.EncodeToString(sigBytes),
			},
		},
		Statement: stmt,
	}
	return bundle, nil
}

// VerificationResult contains the outcome of verifying an attestation bundle.
type VerificationResult struct {
	Valid           bool            `json:"valid"`
	Kind            string          `json:"kind,omitempty"` // KindReport or KindCampaign
	KeyID           string          `json:"key_id"`
	Target          string          `json:"target"`
	Seed            uint64          `json:"seed"`
	Verdict         checker.Verdict `json:"verdict"`
	Fingerprint     string          `json:"fingerprint"`
	HistoryHash     string          `json:"history_hash"`
	SignedAt        time.Time       `json:"signed_at"`
	Signer          string          `json:"signer"`
	HistoryVerified bool            `json:"history_verified,omitempty"`
}

// Verify verifies a report's attestation bundle against an Ed25519 public
// key.
func Verify(b *Bundle, pub ed25519.PublicKey) (*VerificationResult, error) {
	payloadBytes, keyID, err := openEnvelope(b.Payload, b.Signatures, pub)
	if err != nil {
		return &VerificationResult{Valid: false}, err
	}

	var stmt Statement
	if err := json.Unmarshal(payloadBytes, &stmt); err != nil {
		return nil, fmt.Errorf("attest: unmarshal statement payload: %w", err)
	}

	if stmt.Type != StatementTypeV01 {
		return nil, fmt.Errorf("attest: unsupported statement type %q", stmt.Type)
	}
	if stmt.PredicateType != PredicateTypeV1 {
		return nil, fmt.Errorf("attest: unsupported predicate type %q", stmt.PredicateType)
	}

	signedTime, _ := time.Parse(time.RFC3339, stmt.Predicate.SignedAt)

	res := &VerificationResult{
		Valid:       true,
		Kind:        KindReport,
		KeyID:       keyID,
		Target:      stmt.Predicate.Target,
		Seed:        stmt.Predicate.Seed,
		Verdict:     stmt.Predicate.Verdict,
		Fingerprint: stmt.Predicate.Fingerprint,
		HistoryHash: stmt.Predicate.HistoryHash,
		SignedAt:    signedTime,
		Signer:      stmt.Predicate.Signer,
	}

	return res, nil
}

// VerifyHistoryFile checks whether the history.jsonl file on disk matches the signed statement's HistoryHash.
func VerifyHistoryFile(res *VerificationResult, historyPath string) (bool, error) {
	f, err := os.Open(historyPath)
	if err != nil {
		return false, err
	}
	defer f.Close()

	ops, err := history.ReadJSONL(f)
	if err != nil {
		return false, fmt.Errorf("attest: read history file: %w", err)
	}

	calculatedHash := history.Hash(ops)
	if calculatedHash != res.HistoryHash {
		return false, fmt.Errorf("attest: history hash mismatch: signed %q vs file %q", res.HistoryHash, calculatedHash)
	}

	res.HistoryVerified = true
	return true, nil
}

// SignReportFile reads report.json from path, signs it with the private key, and writes bundle.json.
func SignReportFile(reportPath, privKeyPath, outBundlePath, signer string) (*Bundle, error) {
	data, err := os.ReadFile(reportPath)
	if err != nil {
		return nil, err
	}

	var rep report.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("%s: %w", reportPath, err)
	}

	privKey, err := LoadPrivateKey(privKeyPath)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", privKeyPath, err)
	}

	bundle, err := Sign(&rep, privKey, signer)
	if err != nil {
		return nil, err
	}

	if outBundlePath == "" {
		dir := filepath.Dir(reportPath)
		outBundlePath = filepath.Join(dir, "bundle.json")
	}

	bData, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(outBundlePath, bData, 0o644); err != nil {
		return nil, err
	}

	return bundle, nil
}

// VerifyBundleFile reads a report's or a campaign's bundle from path and
// verifies it with the public key; for a report, historyPath (optional)
// must match the signed history digest.
func VerifyBundleFile(bundlePath, pubKeyPath, historyPath string) (*VerificationResult, error) {
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return nil, err
	}

	pubKey, err := LoadPublicKey(pubKeyPath)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", pubKeyPath, err)
	}

	res, err := VerifyAny(data, pubKey)
	if err != nil {
		return res, fmt.Errorf("%s: %w", bundlePath, err)
	}

	if historyPath != "" {
		if res.Kind != KindReport {
			return res, fmt.Errorf("%s: a campaign's statement covers its seeds' histories through the campaign fingerprint; verify a history against its seed's report bundle", bundlePath)
		}
		if _, err := VerifyHistoryFile(res, historyPath); err != nil {
			return res, err
		}
	}

	return res, nil
}
