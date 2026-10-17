package attest

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// PredicateTypeRelease identifies statements that vouch for released files.
const PredicateTypeRelease = "https://strife.dev/release/v1"

// ReleasePredicate records what was built, from what and by whom.
type ReleasePredicate struct {
	Tool     string `json:"tool"`
	Version  string `json:"version"`
	Commit   string `json:"commit,omitempty"`
	Builder  string `json:"builder,omitempty"` // e.g. a CI run URL
	SignedAt string `json:"signed_at"`
	Signer   string `json:"signer"`
}

// ReleaseStatement is an in-toto v0.1 statement over released files.
type ReleaseStatement struct {
	Type          string               `json:"_type"`
	Subject       []ResourceDescriptor `json:"subject"`
	PredicateType string               `json:"predicateType"`
	Predicate     ReleasePredicate     `json:"predicate"`
}

// ReleaseBundle is a signed ReleaseStatement.
type ReleaseBundle struct {
	PayloadType string            `json:"payload_type"`
	Payload     string            `json:"payload"`
	Signatures  []Signature       `json:"signatures"`
	Statement   *ReleaseStatement `json:"statement,omitempty"`
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SignRelease signs the SHA-256 digests of files (named by base name).
func SignRelease(files []string, p ReleasePredicate, priv ed25519.PrivateKey) (*ReleaseBundle, error) {
	if len(files) == 0 {
		return nil, errors.New("attest: no files to sign")
	}
	st := &ReleaseStatement{Type: StatementTypeV01, PredicateType: PredicateTypeRelease, Predicate: p}
	if st.Predicate.Tool == "" {
		st.Predicate.Tool = "strife"
	}
	st.Predicate.SignedAt = time.Now().UTC().Format(time.RFC3339)
	seen := map[string]bool{}
	for _, f := range files {
		name := filepath.Base(f)
		if seen[name] {
			return nil, fmt.Errorf("attest: two files named %s", name)
		}
		seen[name] = true
		sum, err := fileSHA256(f)
		if err != nil {
			return nil, err
		}
		st.Subject = append(st.Subject, ResourceDescriptor{Name: name, Digest: map[string]string{"sha256": sum}})
	}
	sort.Slice(st.Subject, func(i, j int) bool { return st.Subject[i].Name < st.Subject[j].Name })
	payload, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	return &ReleaseBundle{
		PayloadType: PayloadTypeJSON,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  []Signature{{KeyID: KeyID(pub), Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))}},
		Statement:   st,
	}, nil
}

// VerifyRelease checks the bundle's signature with pub, then that every
// signed file in dir exists and matches its digest. It returns the signed
// statement (decoded from the signed payload, never from the unsigned copy).
func VerifyRelease(b *ReleaseBundle, pub ed25519.PublicKey, dir string) (*ReleaseStatement, error) {
	payload, err := base64.StdEncoding.DecodeString(b.Payload)
	if err != nil {
		return nil, fmt.Errorf("attest: invalid payload: %w", err)
	}
	ok := false
	for _, s := range b.Signatures {
		sig, err := base64.StdEncoding.DecodeString(s.Sig)
		if err == nil && ed25519.Verify(pub, payload, sig) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, errors.New("attest: no valid signature for this key")
	}
	var st ReleaseStatement
	if err := json.Unmarshal(payload, &st); err != nil {
		return nil, err
	}
	if st.Type != StatementTypeV01 || st.PredicateType != PredicateTypeRelease {
		return nil, fmt.Errorf("attest: not a release statement (%s, %s)", st.Type, st.PredicateType)
	}
	for _, s := range st.Subject {
		if s.Name != filepath.Base(s.Name) {
			return nil, fmt.Errorf("attest: subject %q is not a plain file name", s.Name)
		}
		sum, err := fileSHA256(filepath.Join(dir, s.Name))
		if err != nil {
			return nil, err
		}
		if sum != s.Digest["sha256"] {
			return nil, fmt.Errorf("attest: %s: sha256 %s, signed %s", s.Name, sum, s.Digest["sha256"])
		}
	}
	return &st, nil
}
