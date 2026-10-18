package campaign

import (
	"fmt"

	"github.com/kemalhafizh/strife/pkg/attest"
)

// Predicate is what signing m vouches for (attest.SignCampaign).
func (m *Manifest) Predicate(signer string) attest.CampaignPredicate {
	return attest.CampaignPredicate{
		Tool:        m.Tool,
		Version:     m.Version,
		System:      m.System,
		Fingerprint: m.Fingerprint,
		Verdict:     m.Verdict(),
		Seeds:       m.TotalSeeds,
		Violations:  m.Violations,
		Ops:         m.TotalOps,
		Faults:      m.Faults,
		Commit:      m.Commit,
		Branch:      m.Branch,
		Signer:      signer,
	}
}

// Attests reports why res does not vouch for m, or nil when it does: it
// must be a valid campaign statement about this campaign's content (its
// recomputed fingerprint), system and verdict. A report's signature never
// vouches for a campaign.
func (m *Manifest) Attests(res *attest.VerificationResult) error {
	switch {
	case res == nil || !res.Valid:
		return fmt.Errorf("campaign: the attestation is not valid")
	case res.Kind != attest.KindCampaign:
		return fmt.Errorf("campaign: the attestation is about a %s, not a campaign", orUnknown(res.Kind))
	case res.Fingerprint != m.ComputeFingerprint():
		return fmt.Errorf("campaign: the attestation is about campaign %.12s, not %.12s", res.Fingerprint, m.ComputeFingerprint())
	case res.Target != m.System || res.Verdict != m.Verdict():
		return fmt.Errorf("campaign: the attestation's system or verdict (%s, %s) differs from the campaign's (%s, %s)", res.Target, res.Verdict, m.System, m.Verdict())
	}
	return nil
}

func orUnknown(kind string) string {
	if kind == "" {
		return "report"
	}
	return kind
}
