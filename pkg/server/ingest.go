package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/attest"
	"github.com/kemalhafizh/strife/pkg/campaign"
	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/report"
)

// Upload is the body of POST /api/v1/projects/{project}/runs: one report
// (with its history and attestation when available) or one campaign
// manifest, plus where it came from.
type Upload struct {
	Report   json.RawMessage `json:"report,omitempty"`
	Campaign json.RawMessage `json:"campaign,omitempty"`
	History  string          `json:"history,omitempty"` // history.jsonl content
	Bundle   json.RawMessage `json:"bundle,omitempty"`  // bundle.json from strife sign
	Commit   string          `json:"commit,omitempty"`
	Branch   string          `json:"branch,omitempty"`
	Source   string          `json:"source,omitempty"`
}

// rejection is an upload the server refuses, with the reason shown to the
// client (HTTP 422).
type rejection struct{ msg string }

func (r *rejection) Error() string { return r.msg }

func reject(format string, a ...any) error { return &rejection{fmt.Sprintf(format, a...)} }

// ingested is a verified upload, ready to store.
type ingested struct {
	run   *Run
	files map[string][]byte
	camp  *campaign.Manifest
}

// verify checks an upload's integrity: the fingerprint must match the
// content, the history must hash to what the report says, and a signature
// must come from one of the project's trusted keys. Anything that does not
// verify is refused rather than stored with a warning.
func verify(p *project, u *Upload, now time.Time) (*ingested, error) {
	if (len(u.Report) == 0) == (len(u.Campaign) == 0) {
		return nil, reject("send exactly one of report and campaign")
	}
	in := &ingested{files: map[string][]byte{}}
	r := &Run{Project: p.name, Received: now.UTC(), Commit: clip(u.Commit, 100), Branch: clip(u.Branch, 200), Source: clip(u.Source, 500)}
	in.run = r

	if len(u.Campaign) > 0 {
		var m campaign.Manifest
		if err := strictJSON(u.Campaign, &m); err != nil {
			return nil, reject("campaign: %v", err)
		}
		if m.Fingerprint == "" || m.ComputeFingerprint() != m.Fingerprint {
			return nil, reject("campaign: fingerprint does not match its content")
		}
		if u.History != "" {
			return nil, reject("campaign: histories are pushed with the seeds' reports, not with the manifest")
		}
		if len(u.Bundle) > 0 {
			res, err := verifySignature(p, u.Bundle)
			if err != nil {
				return nil, err
			}
			if err := m.Attests(res); err != nil {
				return nil, reject("bundle: %v", err)
			}
			r.Attested, r.Signer, r.KeyID = true, clip(res.Signer, 200), res.KeyID
			in.files[fileBundle] = u.Bundle
		} else if p.requireSignature {
			return nil, reject("this project requires signed runs: sign the campaign (strife sign --campaign) and push campaign-bundle.json with it")
		}
		r.ID, r.Kind, r.Target = m.Fingerprint, "campaign", m.System
		r.Verdict, r.Seeds, r.Violations, r.Ops = m.Verdict(), m.TotalSeeds, m.Violations, m.TotalOps
		r.Faults = append([]nemesis.Kind(nil), m.Faults...)
		if r.Commit == "" {
			r.Commit = clip(m.Commit, 100)
		}
		if r.Branch == "" {
			r.Branch = clip(m.Branch, 200)
		}
		r.Series = series(r.Kind, r.Target, r.Faults)
		in.files[fileCampaign] = u.Campaign
		in.camp = &m
		return in, nil
	}

	var rep report.Report
	if err := strictJSON(u.Report, &rep); err != nil {
		return nil, reject("report: %v", err)
	}
	if rep.Fingerprint == "" || report.Fingerprint(&rep) != rep.Fingerprint {
		return nil, reject("report: fingerprint does not match its content (edited or truncated?)")
	}
	r.ID, r.Kind, r.Target, r.Seed, r.Verdict = rep.Fingerprint, rep.Kind, rep.Target, rep.Seed, rep.Verdict
	r.Ops = rep.Check.Stats.Invocations
	for _, f := range rep.Schedule.Faults {
		if !slices.Contains(r.Faults, f.Kind) {
			r.Faults = append(r.Faults, f.Kind)
		}
	}
	slices.Sort(r.Faults)
	r.Series = series(r.Kind, r.Target, r.Faults)
	in.files[fileReport] = u.Report

	if u.History != "" {
		ops, err := history.ReadJSONL(strings.NewReader(u.History))
		if err != nil {
			return nil, reject("history: %v", err)
		}
		if history.Hash(ops) != rep.HistoryHash {
			return nil, reject("history: its SHA-256 does not match the report's history_hash")
		}
		r.HasHistory, r.HistoryVerified = true, true
		in.files[fileHistory] = []byte(u.History)
	}

	if len(u.Bundle) > 0 {
		res, err := verifySignature(p, u.Bundle)
		if err != nil {
			return nil, err
		}
		if res.Kind != attest.KindReport || res.Fingerprint != rep.Fingerprint || res.Target != rep.Target || res.Verdict != rep.Verdict || res.HistoryHash != rep.HistoryHash {
			return nil, reject("bundle: the signed statement is about a different report")
		}
		r.Attested, r.Signer, r.KeyID = true, clip(res.Signer, 200), res.KeyID
		in.files[fileBundle] = u.Bundle
	} else if p.requireSignature {
		return nil, reject("this project requires runs signed by a trusted key (strife sign)")
	}
	return in, nil
}

// verifySignature accepts a report's or a campaign's bundle signed by any
// of the project's keys and returns what it vouches for; the caller checks
// that it is about the run pushed with it.
func verifySignature(p *project, bundle []byte) (*attest.VerificationResult, error) {
	if len(p.keys) == 0 {
		return nil, reject("bundle: this project trusts no signing key; add one to trusted_keys")
	}
	if err := strictJSON(bundle, new(json.RawMessage)); err != nil {
		return nil, reject("bundle: %v", err)
	}
	var lastErr error
	for _, k := range p.keys {
		res, err := attest.VerifyAny(bundle, k)
		if err == nil && res.Valid {
			return res, nil
		}
		lastErr = err
	}
	if lastErr != nil && !errors.Is(lastErr, attest.ErrSignature) {
		return nil, reject("bundle: %v", lastErr)
	}
	return nil, reject("bundle: not signed by any key this project trusts")
}

// series groups runs that are compared with each other: same kind, target
// and fault kinds.
func series(kind, target string, faults []nemesis.Kind) string {
	f := make([]string, len(faults))
	for i, k := range faults {
		f[i] = string(k)
	}
	return kind + "/" + target + "/" + strings.Join(f, ",")
}

// compare sets r.Change against the previous run of its series.
func compare(r *Run, prev *Run, prevCamp, camp *campaign.Manifest) {
	if prev == nil {
		r.Change = ChangeNew
		return
	}
	r.Previous = prev.ID
	if camp != nil && prevCamp != nil {
		d := campaign.Compare(prevCamp, camp)
		r.Diff = d
		switch {
		case d.HasRegressions():
			r.Change = ChangeRegression
		case prev.Verdict == checker.Violation && r.Verdict == checker.Linearizable:
			r.Change = ChangeFixed
		}
		return
	}
	switch {
	case prev.Verdict != checker.Violation && r.Verdict == checker.Violation:
		r.Change = ChangeRegression
	case prev.Verdict == checker.Violation && r.Verdict == checker.Linearizable:
		r.Change = ChangeFixed
	}
}

func strictJSON(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
