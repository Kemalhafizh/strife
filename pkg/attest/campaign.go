package attest

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/report"
)

// PredicateTypeCampaign identifies statements that vouch for a campaign.
const PredicateTypeCampaign = "https://strife.dev/attestation/campaign/v1"

// Kinds of signed statements a VerificationResult can come from.
const (
	KindReport   = "report"
	KindCampaign = "campaign"
)

// CampaignPredicate is what a signer vouches for about a campaign. The
// fingerprint covers the system, the configuration and every seed's
// verdict and trace, history and schedule hashes; the other fields repeat
// what readers show, so they can be checked against the manifest.
type CampaignPredicate struct {
	Tool        string          `json:"tool"`
	Version     string          `json:"version"`
	System      string          `json:"system"`
	Fingerprint string          `json:"fingerprint"`
	Verdict     checker.Verdict `json:"verdict"`
	Seeds       int             `json:"seeds"`
	Violations  int             `json:"violations"`
	Ops         int             `json:"ops"`
	Faults      []nemesis.Kind  `json:"faults,omitempty"`
	Commit      string          `json:"commit,omitempty"`
	Branch      string          `json:"branch,omitempty"`
	SignedAt    string          `json:"signed_at"`
	Signer      string          `json:"signer"`
}

// CampaignStatement is an in-toto v0.1 statement about a campaign.
type CampaignStatement struct {
	Type          string               `json:"_type"`
	Subject       []ResourceDescriptor `json:"subject"`
	PredicateType string               `json:"predicateType"`
	Predicate     CampaignPredicate    `json:"predicate"`
}

// CampaignBundle is a signed CampaignStatement, in the same envelope as a
// report's Bundle.
type CampaignBundle struct {
	PayloadType string             `json:"payload_type"`
	Payload     string             `json:"payload"`
	Signatures  []Signature        `json:"signatures"`
	Statement   *CampaignStatement `json:"statement,omitempty"`
}

// SignCampaign signs a campaign's predicate, which the caller fills in from
// the campaign manifest (campaign.Manifest.Predicate).
func SignCampaign(p CampaignPredicate, priv ed25519.PrivateKey) (*CampaignBundle, error) {
	if p.Fingerprint == "" || p.System == "" {
		return nil, errors.New("attest: a campaign statement needs the campaign's system and fingerprint")
	}
	if p.Tool == "" {
		p.Tool = "strife"
	}
	if p.Version == "" {
		p.Version = report.Version
	}
	if p.Signer == "" {
		p.Signer = "strife-auditor"
	}
	p.SignedAt = time.Now().UTC().Format(time.RFC3339)
	stmt := &CampaignStatement{
		Type:          StatementTypeV01,
		Subject:       []ResourceDescriptor{{Name: p.System, Digest: map[string]string{"campaign_sha256": p.Fingerprint}}},
		PredicateType: PredicateTypeCampaign,
		Predicate:     p,
	}
	payload, err := json.Marshal(stmt)
	if err != nil {
		return nil, fmt.Errorf("attest: marshal statement: %w", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	return &CampaignBundle{
		PayloadType: PayloadTypeJSON,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  []Signature{{KeyID: KeyID(pub), Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))}},
		Statement:   stmt,
	}, nil
}

// VerifyCampaign checks that pub signed b and returns the statement as
// signed (the payload, not the bundle's unsigned copy) and its summary.
func VerifyCampaign(b *CampaignBundle, pub ed25519.PublicKey) (*CampaignStatement, *VerificationResult, error) {
	payload, keyID, err := openEnvelope(b.Payload, b.Signatures, pub)
	if err != nil {
		return nil, &VerificationResult{Valid: false}, err
	}
	var stmt CampaignStatement
	if err := json.Unmarshal(payload, &stmt); err != nil {
		return nil, nil, fmt.Errorf("attest: unmarshal statement payload: %w", err)
	}
	if stmt.Type != StatementTypeV01 {
		return nil, nil, fmt.Errorf("attest: unsupported statement type %q", stmt.Type)
	}
	if stmt.PredicateType != PredicateTypeCampaign {
		return nil, nil, fmt.Errorf("attest: not a campaign statement (predicate type %q)", stmt.PredicateType)
	}
	signedAt, _ := time.Parse(time.RFC3339, stmt.Predicate.SignedAt)
	return &stmt, &VerificationResult{
		Valid:       true,
		Kind:        KindCampaign,
		KeyID:       keyID,
		Target:      stmt.Predicate.System,
		Verdict:     stmt.Predicate.Verdict,
		Fingerprint: stmt.Predicate.Fingerprint,
		SignedAt:    signedAt,
		Signer:      stmt.Predicate.Signer,
	}, nil
}

// VerifyAny verifies a report's or a campaign's bundle, whichever data
// holds; the result's Kind says which.
func VerifyAny(data []byte, pub ed25519.PublicKey) (*VerificationResult, error) {
	var env struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("attest: not a bundle: %w", err)
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, fmt.Errorf("attest: invalid base64 payload: %w", err)
	}
	var head struct {
		PredicateType string `json:"predicateType"`
	}
	if err := json.Unmarshal(payload, &head); err != nil {
		return nil, fmt.Errorf("attest: unmarshal statement payload: %w", err)
	}
	switch head.PredicateType {
	case PredicateTypeV1:
		var b Bundle
		if err := json.Unmarshal(data, &b); err != nil {
			return nil, err
		}
		return Verify(&b, pub)
	case PredicateTypeCampaign:
		var b CampaignBundle
		if err := json.Unmarshal(data, &b); err != nil {
			return nil, err
		}
		_, res, err := VerifyCampaign(&b, pub)
		return res, err
	case PredicateTypeRelease:
		return nil, errors.New("attest: a release bundle; verify it against the released files (--release-dir)")
	}
	return nil, fmt.Errorf("attest: unsupported predicate type %q", head.PredicateType)
}

// ErrSignature means a bundle's signature does not verify with the key: the
// key did not sign it, or the payload changed since.
var ErrSignature = errors.New("attest: invalid cryptographic signature (verification failed)")

// openEnvelope checks the signature by pub over a bundle's payload and
// returns the payload and the signature's key ID.
func openEnvelope(payloadB64 string, sigs []Signature, pub ed25519.PublicKey) ([]byte, string, error) {
	if payloadB64 == "" {
		return nil, "", errors.New("attest: empty bundle payload")
	}
	if len(sigs) == 0 {
		return nil, "", errors.New("attest: no signatures in bundle")
	}
	payload, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, "", fmt.Errorf("attest: invalid base64 payload: %w", err)
	}
	sig := sigs[0]
	for _, s := range sigs {
		if s.KeyID == KeyID(pub) {
			sig = s
			break
		}
	}
	raw, err := base64.StdEncoding.DecodeString(sig.Sig)
	if err != nil {
		return nil, "", fmt.Errorf("attest: invalid base64 signature: %w", err)
	}
	if !ed25519.Verify(pub, payload, raw) {
		return nil, "", ErrSignature
	}
	return payload, sig.KeyID, nil
}
