package bridge

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/kemalhafizh/strife/pkg/attest"
	"github.com/kemalhafizh/strife/pkg/history"
)

// AttestationResult is the outcome of VerifyAttestation. Every field except
// Valid, Problem, KeyID and BundleKeys comes from the signed payload, never
// from the bundle's unsigned copy of the statement.
type AttestationResult struct {
	Valid          bool     `json:"valid"`
	Problem        string   `json:"problem,omitempty"`
	KeyID          string   `json:"key_id"`      // fingerprint of the key given
	BundleKeys     []string `json:"bundle_keys"` // fingerprints the bundle was signed with
	Signer         string   `json:"signer,omitempty"`
	SignedAt       string   `json:"signed_at,omitempty"`
	Target         string   `json:"target,omitempty"`
	Seed           uint64   `json:"seed,omitempty"`
	Verdict        string   `json:"verdict,omitempty"`
	Fingerprint    string   `json:"fingerprint,omitempty"`
	HistoryHash    string   `json:"history_hash,omitempty"`
	HistoryChecked bool     `json:"history_checked"`
	HistoryMatches bool     `json:"history_matches"`
	HistoryActual  string   `json:"history_actual,omitempty"`
}

// VerifyAttestation checks a bundle.json written by `strife sign` against an
// Ed25519 public key, and, when history is not empty, that the history.jsonl
// is the one that was signed. It is `strife verify --bundle --key
// [--history]` without the file system.
func VerifyAttestation(bundle, key, hist []byte) (*AttestationResult, error) {
	pub, err := parsePublicKey(key)
	if err != nil {
		return nil, err
	}
	var b attest.Bundle
	if err := json.Unmarshal(bundle, &b); err != nil {
		return nil, fmt.Errorf("not an attestation bundle: %w", err)
	}
	out := &AttestationResult{KeyID: attest.KeyID(pub)}
	for _, s := range b.Signatures {
		out.BundleKeys = append(out.BundleKeys, s.KeyID)
	}
	res, err := attest.Verify(&b, pub)
	if err != nil {
		out.Problem = err.Error()
		return out, nil
	}
	out.Valid = res.Valid
	out.Signer = res.Signer
	if !res.SignedAt.IsZero() {
		out.SignedAt = res.SignedAt.UTC().Format("2006-01-02 15:04:05 UTC")
	}
	out.Target = res.Target
	out.Seed = res.Seed
	out.Verdict = string(res.Verdict)
	out.Fingerprint = res.Fingerprint
	out.HistoryHash = res.HistoryHash
	if len(hist) > 0 {
		ops, err := history.ReadJSONL(bytes.NewReader(hist))
		if err != nil {
			return nil, fmt.Errorf("history: %w", err)
		}
		out.HistoryChecked = true
		out.HistoryActual = history.Hash(ops)
		out.HistoryMatches = out.HistoryActual == res.HistoryHash
		if !out.HistoryMatches {
			out.Valid = false
			out.Problem = "the history file is not the one that was signed"
		}
	}
	return out, nil
}

// NamedFile is a file the visitor picked, with its base name.
type NamedFile struct {
	Name string `json:"name"`
	Data []byte `json:"-"`
}

// FileResult says whether one picked file is part of a signed release.
type FileResult struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Status string `json:"status"` // "match", "mismatch" or "not-in-release"
}

// ReleaseResult is the outcome of VerifyRelease.
type ReleaseResult struct {
	Valid      bool         `json:"valid"`
	Problem    string       `json:"problem,omitempty"`
	KeyID      string       `json:"key_id"`
	BundleKeys []string     `json:"bundle_keys"`
	Version    string       `json:"version,omitempty"`
	Commit     string       `json:"commit,omitempty"`
	Builder    string       `json:"builder,omitempty"`
	Signer     string       `json:"signer,omitempty"`
	SignedAt   string       `json:"signed_at,omitempty"`
	Subjects   []string     `json:"subjects,omitempty"`
	Files      []FileResult `json:"files"`
}

// VerifyRelease checks a release attestation (strife_<version>.intoto.json)
// against a public key, then compares each picked file with the digest the
// release signed for its name. Unlike `strife verify --release-dir`, which
// wants every released file, it checks only the files given: a visitor
// usually downloads one archive.
func VerifyRelease(bundle, key []byte, files []NamedFile) (*ReleaseResult, error) {
	pub, err := parsePublicKey(key)
	if err != nil {
		return nil, err
	}
	var b attest.ReleaseBundle
	if err := json.Unmarshal(bundle, &b); err != nil {
		return nil, fmt.Errorf("not a release attestation: %w", err)
	}
	out := &ReleaseResult{KeyID: attest.KeyID(pub), Files: []FileResult{}}
	for _, s := range b.Signatures {
		out.BundleKeys = append(out.BundleKeys, s.KeyID)
	}
	// The same checks as attest.VerifyRelease, in the same order.
	payload, err := base64.StdEncoding.DecodeString(b.Payload)
	if err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	signed := false
	for _, s := range b.Signatures {
		sig, err := base64.StdEncoding.DecodeString(s.Sig)
		if err == nil && ed25519.Verify(pub, payload, sig) {
			signed = true
			break
		}
	}
	if !signed {
		out.Problem = "no valid signature for this key"
		return out, nil
	}
	var st attest.ReleaseStatement
	if err := json.Unmarshal(payload, &st); err != nil {
		return nil, err
	}
	if st.Type != attest.StatementTypeV01 || st.PredicateType != attest.PredicateTypeRelease {
		return nil, fmt.Errorf("not a release statement (%s, %s)", st.Type, st.PredicateType)
	}
	digests := map[string]string{}
	for _, s := range st.Subject {
		if s.Name != filepath.Base(s.Name) {
			return nil, fmt.Errorf("subject %q is not a plain file name", s.Name)
		}
		digests[s.Name] = s.Digest["sha256"]
		out.Subjects = append(out.Subjects, s.Name)
	}
	out.Valid = true
	out.Version = st.Predicate.Version
	out.Commit = st.Predicate.Commit
	out.Builder = st.Predicate.Builder
	out.Signer = st.Predicate.Signer
	out.SignedAt = st.Predicate.SignedAt
	for _, f := range files {
		sum := sha256.Sum256(f.Data)
		r := FileResult{Name: filepath.Base(f.Name), SHA256: hex.EncodeToString(sum[:])}
		want, ok := digests[r.Name]
		switch {
		case !ok:
			r.Status = "not-in-release"
		case want == r.SHA256:
			r.Status = "match"
		default:
			r.Status = "mismatch"
		}
		if r.Status != "match" {
			out.Valid = false
		}
		out.Files = append(out.Files, r)
	}
	return out, nil
}

// parsePublicKey accepts what attest.LoadPublicKey accepts: a PEM "PUBLIC
// KEY" block as written by `strife keygen`, or the raw 32 key bytes.
func parsePublicKey(data []byte) (ed25519.PublicKey, error) {
	if block, _ := pem.Decode(data); block != nil {
		k, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("public key: %w", err)
		}
		pub, ok := k.(ed25519.PublicKey)
		if !ok {
			return nil, errors.New("public key: not an Ed25519 key")
		}
		return pub, nil
	}
	if len(data) == ed25519.PublicKeySize {
		return ed25519.PublicKey(data), nil
	}
	return nil, errors.New("public key: expected a PEM file from `strife keygen` (.pub)")
}
