// Package compliance maps empirical Strife chaos testing and linearizability
// verification artifacts to technical resilience and data-integrity controls
// across regulatory frameworks (EU DORA, AICPA SOC 2, ISO/IEC 27001:2022, and
// Bank Indonesia / OJK IT Risk Management).
//
// In accordance with Strife's honesty guidelines, a compliance dossier never
// claims legal certification by guesswork: every control evaluation cites the
// exact technical evidence (verdict, fault classes exercised, operation counts,
// and cryptographic hashes) and explicitly documents what was tested, what was
// not tested, and the boundaries between simulation and live runs.
package compliance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/attest"
	"github.com/kemalhafizh/strife/pkg/campaign"
	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/report"
)

// FrameworkID identifies a regulatory or audit standard.
type FrameworkID string

// Supported regulatory and audit frameworks.
const (
	FrameworkDORA     FrameworkID = "dora"
	FrameworkSOC2     FrameworkID = "soc2"
	FrameworkISO27001 FrameworkID = "iso27001"
	FrameworkBIOJK    FrameworkID = "bi-ojk"
)

// AllFrameworks returns all supported regulatory framework identifiers.
func AllFrameworks() []FrameworkID {
	return []FrameworkID{
		FrameworkDORA,
		FrameworkSOC2,
		FrameworkISO27001,
		FrameworkBIOJK,
	}
}

// ParseFrameworks parses a comma-separated list of frameworks (or "all").
func ParseFrameworks(s string) ([]FrameworkID, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "all" {
		return AllFrameworks(), nil
	}
	var out []FrameworkID
	seen := make(map[FrameworkID]bool)
	for _, part := range strings.Split(s, ",") {
		id := FrameworkID(strings.ToLower(strings.TrimSpace(part)))
		if id == "" {
			continue
		}
		switch id {
		case FrameworkDORA, FrameworkSOC2, FrameworkISO27001, FrameworkBIOJK:
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		default:
			return nil, fmt.Errorf("unknown compliance framework %q (valid: dora, soc2, iso27001, bi-ojk, all)", part)
		}
	}
	if len(out) == 0 {
		return AllFrameworks(), nil
	}
	return out, nil
}

// ControlStatus represents the technical verification outcome of a control.
type ControlStatus string

// Possible control statuses.
const (
	StatusPass    ControlStatus = "pass"
	StatusPartial ControlStatus = "partial"
	StatusFail    ControlStatus = "fail"
)

// ControlResult records the evaluation of a single technical control requirement.
type ControlResult struct {
	Framework   FrameworkID   `json:"framework"`
	ControlID   string        `json:"control_id"`
	Title       string        `json:"title"`
	Requirement string        `json:"requirement"`
	Status      ControlStatus `json:"status"`
	Evidence    string        `json:"evidence"`
	Gap         string        `json:"gap,omitempty"`
}

// EvidenceSummary aggregates the empirical test facts backing the dossier.
type EvidenceSummary struct {
	Target              string          `json:"target"`
	RunKind             string          `json:"run_kind"`
	Model               string          `json:"model"`
	Verdict             checker.Verdict `json:"verdict"`
	SeedsTested         int             `json:"seeds_tested"`
	SeedsLinearizable   int             `json:"seeds_linearizable"`
	SeedsViolating      int             `json:"seeds_violating"`
	TotalOperations     int             `json:"total_operations"`
	Nodes               int             `json:"nodes"`
	FaultKindsExercised []nemesis.Kind  `json:"fault_kinds_exercised"`
	FaultCount          int             `json:"fault_count"`
	ProbeEvents         int             `json:"probe_events,omitempty"`
	Fingerprint         string          `json:"fingerprint"`
	HistoryHash         string          `json:"history_hash,omitempty"`
	ScheduleHash        string          `json:"schedule_hash,omitempty"`
	TraceHash           string          `json:"trace_hash,omitempty"`
	Attested            bool            `json:"attested"`
	Signer              string          `json:"signer,omitempty"`
	KeyID               string          `json:"key_id,omitempty"`
}

// Methodology documents the exact technical scope and boundaries of the test.
type Methodology struct {
	WhatIsTested      []string `json:"what_is_tested"`
	WhatIsNotTested   []string `json:"what_is_not_tested"`
	ExecutionLimits   []string `json:"execution_limits"`
	AuditorDisclaimer string   `json:"auditor_disclaimer"`
}

// Dossier is the complete regulatory compliance and resilience audit package.
type Dossier struct {
	Tool        string          `json:"tool"`
	Version     string          `json:"version"`
	GeneratedAt string          `json:"generated_at"`
	Overall     ControlStatus   `json:"overall"`
	DossierHash string          `json:"dossier_hash"`
	Frameworks  []FrameworkID   `json:"frameworks"`
	Summary     EvidenceSummary `json:"summary"`
	Controls    []ControlResult `json:"controls"`
	Methodology Methodology     `json:"methodology"`
}

// Options configure how a Dossier is built.
type Options struct {
	Report       *report.Report
	Campaign     *campaign.Manifest
	Verification *attest.VerificationResult
	Frameworks   []FrameworkID
	Now          time.Time
}

// Evaluate builds a regulatory compliance Dossier from a Strife report or campaign.
func Evaluate(opts Options) (*Dossier, error) {
	if opts.Report == nil && opts.Campaign == nil {
		return nil, fmt.Errorf("compliance: either Report or Campaign is required")
	}
	fws := opts.Frameworks
	if len(fws) == 0 {
		fws = AllFrameworks()
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	sum := extractEvidence(opts.Report, opts.Campaign, opts.Verification)
	meth := buildMethodology(sum)

	var controls []ControlResult
	for _, fw := range fws {
		controls = append(controls, evaluateFramework(fw, sum)...)
	}

	overall := StatusPass
	for _, c := range controls {
		if c.Status == StatusFail {
			overall = StatusFail
			break
		}
		if c.Status == StatusPartial && overall == StatusPass {
			overall = StatusPartial
		}
	}

	d := &Dossier{
		Tool:        "strife",
		Version:     report.Version,
		GeneratedAt: now.Format(time.RFC3339),
		Overall:     overall,
		Frameworks:  fws,
		Summary:     sum,
		Controls:    controls,
		Methodology: meth,
	}
	d.DossierHash = HashDossier(d)
	return d, nil
}

// HashDossier computes a tamper-evident SHA-256 digest over the dossier's
// evidence summary and control evaluations.
func HashDossier(d *Dossier) string {
	h := sha256.New()
	fmt.Fprintf(h, "strife-compliance-dossier-v1\n%s\n%s\n%s\n", d.Summary.Target, d.Summary.Fingerprint, d.Overall)
	for _, c := range d.Controls {
		fmt.Fprintf(h, "%s:%s:%s:%s\n", c.Framework, c.ControlID, c.Status, c.Evidence)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func extractEvidence(rep *report.Report, camp *campaign.Manifest, ver *attest.VerificationResult) EvidenceSummary {
	var sum EvidenceSummary
	faultSet := make(map[nemesis.Kind]bool)

	if rep != nil {
		modelName := rep.Check.Model
		if modelName == "" {
			modelName = "kv"
		}
		sum = EvidenceSummary{
			Target:          rep.Target,
			RunKind:         rep.Kind,
			Model:           modelName,
			Verdict:         rep.Verdict,
			SeedsTested:     1,
			TotalOperations: rep.Check.Operations,
			Nodes:           rep.Nodes,
			FaultCount:      len(rep.Schedule.Faults),
			ProbeEvents:     len(rep.Probes),
			Fingerprint:     rep.Fingerprint,
			HistoryHash:     rep.HistoryHash,
			ScheduleHash:    rep.ScheduleHash,
			TraceHash:       rep.TraceHash,
		}
		if rep.Verdict == checker.Linearizable {
			sum.SeedsLinearizable = 1
		} else if rep.Verdict == checker.Violation {
			sum.SeedsViolating = 1
		}
		for _, f := range rep.Schedule.Faults {
			faultSet[f.Kind] = true
		}
	} else if camp != nil {
		faultCount := 0
		for _, s := range camp.Seeds {
			faultCount += s.Faults
		}
		sum = EvidenceSummary{
			Target:            camp.System,
			RunKind:           "campaign",
			Model:             "kv",
			Verdict:           camp.Verdict(),
			SeedsTested:       camp.TotalSeeds,
			SeedsLinearizable: camp.Passed,
			SeedsViolating:    camp.Violations,
			TotalOperations:   camp.TotalOps,
			Nodes:             camp.Config.Nodes,
			FaultCount:        faultCount,
			Fingerprint:       camp.Fingerprint,
		}
		for _, k := range camp.Faults {
			faultSet[k] = true
		}
		for _, k := range camp.Config.Faults {
			faultSet[k] = true
		}
	}

	for k := range faultSet {
		sum.FaultKindsExercised = append(sum.FaultKindsExercised, k)
	}
	sort.Slice(sum.FaultKindsExercised, func(i, j int) bool {
		return sum.FaultKindsExercised[i] < sum.FaultKindsExercised[j]
	})

	if ver != nil && ver.Valid {
		sum.Attested = true
		sum.Signer = ver.Signer
		sum.KeyID = ver.KeyID
	}
	return sum
}

func hasAnyFault(exercised []nemesis.Kind, targets ...nemesis.Kind) bool {
	for _, ex := range exercised {
		for _, t := range targets {
			if ex == t {
				return true
			}
		}
	}
	return false
}

func formatFaultList(kinds []nemesis.Kind) string {
	if len(kinds) == 0 {
		return "none"
	}
	s := make([]string, len(kinds))
	for i, k := range kinds {
		s[i] = string(k)
	}
	return strings.Join(s, ", ")
}

func evaluateFramework(fw FrameworkID, sum EvidenceSummary) []ControlResult {
	hasNetFault := hasAnyFault(sum.FaultKindsExercised, nemesis.Partition, nemesis.Latency, nemesis.Loss)
	hasCrashFault := hasAnyFault(sum.FaultKindsExercised, nemesis.Kill, nemesis.Pause, nemesis.Freeze)
	hasStorageFault := hasAnyFault(sum.FaultKindsExercised, nemesis.IOStall, nemesis.Kill)
	hasClockFault := hasAnyFault(sum.FaultKindsExercised, nemesis.ClockSkew)
	isClean := sum.Verdict == checker.Linearizable && sum.SeedsViolating == 0

	switch fw {
	case FrameworkDORA:
		return []ControlResult{
			evalControl(
				FrameworkDORA,
				"DORA-Art.24-25",
				"Digital Operational Resilience Testing",
				"ICT systems supporting critical functions must undergo resilience testing under adverse network and infrastructure disruption scenarios.",
				isClean,
				hasNetFault && hasCrashFault,
				fmt.Sprintf("Target %q (%s) tested across %d seed(s) and %d operations with fault classes [%s]; verdict=%s.",
					sum.Target, sum.RunKind, sum.SeedsTested, sum.TotalOperations, formatFaultList(sum.FaultKindsExercised), sum.Verdict),
				"Exercise both network faults (partition/latency/loss) and process/node faults (kill/pause/freeze) to achieve full coverage.",
			),
			evalControl(
				FrameworkDORA,
				"DORA-Art.9.4(c)",
				"Protection of Data Integrity Under Disruption",
				"ICT systems must prevent data corruption, stale reads, lost updates, or split-brain divergence during network or storage failures.",
				isClean,
				sum.TotalOperations > 0,
				fmt.Sprintf("Model %q verified %d client operations across %d seed(s): %d linearizable, %d violating (fingerprint %s).",
					sum.Model, sum.TotalOperations, sum.SeedsTested, sum.SeedsLinearizable, sum.SeedsViolating, shortHash(sum.Fingerprint)),
				"Run a non-empty client workload and resolve all consistency violations.",
			),
			evalControl(
				FrameworkDORA,
				"DORA-Art.11-12",
				"Crash Recovery & Storage Durability Verification",
				"Recovery procedures and write-ahead log / fsync durability must preserve committed data across sudden termination and I/O stalls.",
				isClean,
				hasStorageFault,
				fmt.Sprintf("Storage/crash fault coverage: [%s] on %d-node cluster; verdict=%s.",
					formatFaultList(sum.FaultKindsExercised), sum.Nodes, sum.Verdict),
				"Include 'kill' and 'io-stall' in the fault schedule to verify WAL/fsync durability under sudden crashes.",
			),
		}

	case FrameworkSOC2:
		return []ControlResult{
			evalControl(
				FrameworkSOC2,
				"PI1.3-PI1.4",
				"Processing Integrity (Completeness & Accuracy)",
				"System processing is complete, valid, accurate, and authorized without anomaly cycles or stale state transitions.",
				isClean,
				sum.TotalOperations > 0,
				fmt.Sprintf("WGL/Elle formal checker verified %d operations against %q specification; verdict=%s.",
					sum.TotalOperations, sum.Model, sum.Verdict),
				"Ensure all checked keys/transactions satisfy linearizability or target isolation without violations.",
			),
			evalControl(
				FrameworkSOC2,
				"A1.2",
				"Availability & Environmental Disruption Recovery",
				"System maintains safety invariants and recovers processing capacity during network partitions and node failures.",
				isClean,
				len(sum.FaultKindsExercised) > 0,
				fmt.Sprintf("Executed %d fault injection transition(s) across fault classes [%s] on %d nodes.",
					sum.FaultCount, formatFaultList(sum.FaultKindsExercised), sum.Nodes),
				"Enable active fault injection (--faults partition,kill,pause) rather than a fault-free baseline.",
			),
			evalControl(
				FrameworkSOC2,
				"CC7.2-CC7.4",
				"Tamper-Evident Audit Evidence & Root-Cause Telemetry",
				"Security and resilience test artifacts are cryptographically bound and attributable to an authorized verifier.",
				true,
				sum.Attested,
				attestEvidenceString(sum),
				"Sign the report with 'strife sign --report report.json --key auditor.key' and pass '--bundle bundle.json --key auditor.pub' to bind cryptographic non-repudiation.",
			),
		}

	case FrameworkISO27001:
		return []ControlResult{
			evalControl(
				FrameworkISO27001,
				"A.8.13-A.8.14",
				"Information Backup & Redundancy of Processing Facilities",
				"Redundant cluster nodes preserve committed state and quorum safety when minority nodes fail or disconnect.",
				isClean,
				sum.Nodes >= 3 && (hasNetFault || hasCrashFault),
				fmt.Sprintf("Cluster of %d node(s) tested under [%s]; verdict=%s.",
					sum.Nodes, formatFaultList(sum.FaultKindsExercised), sum.Verdict),
				"Test on a multi-node cluster (>= 3 nodes) with partition or crash faults enabled.",
			),
			evalControl(
				FrameworkISO27001,
				"A.8.17",
				"Clock Synchronization & Drift Resilience",
				"Distributed coordination and leader leases remain safe under local clock skew or wall-clock jumps.",
				isClean,
				hasClockFault,
				fmt.Sprintf("Clock fault exercised=%v (faults: [%s]); verdict=%s.",
					hasClockFault, formatFaultList(sum.FaultKindsExercised), sum.Verdict),
				"Include 'clock-skew' in the fault schedule to verify that leader leases and timestamps do not depend on unsynchronized wall clocks.",
			),
			evalControl(
				FrameworkISO27001,
				"A.8.29",
				"Security & Resilience Testing in Development and Acceptance",
				"Automated, reproducible chaos and consistency verification gates are executed with deterministic seed tracking.",
				isClean,
				sum.Fingerprint != "",
				fmt.Sprintf("Deterministic run fingerprint %s (%d seed(s), %d operations).",
					shortHash(sum.Fingerprint), sum.SeedsTested, sum.TotalOperations),
				"Retain report artifacts and SHA-256 fingerprints in CI.",
			),
		}

	case FrameworkBIOJK:
		return []ControlResult{
			evalControl(
				FrameworkBIOJK,
				"SEOJK-29/MRTIK-DRC",
				"Ketahanan Sistem & Uji Pemulihan Bencana (Failover / Split-Brain)",
				"Sistem pemrosesan transaksi mampu bertahan terhadap gangguan jaringan (partisi/latensi) dan kegagalan node tanpa mengalami split-brain.",
				isClean,
				hasNetFault && hasCrashFault,
				fmt.Sprintf("Target %q diuji pada %d node dengan injeksi gangguan [%s] (%d seed, %d operasi); hasil=%s.",
					sum.Target, sum.Nodes, formatFaultList(sum.FaultKindsExercised), sum.SeedsTested, sum.TotalOperations, sum.Verdict),
				"Sertakan kombinasi gangguan jaringan (partition) dan kegagalan proses (kill/pause) pada pengujian.",
			),
			evalControl(
				FrameworkBIOJK,
				"SEOJK-29/Integritas-Data",
				"Integritas & Konsistensi Data Transaksi",
				"Tidak terdapat kehilangan transaksi yang telah diakui (lost update), pembacaan basi (stale read), maupun anomali siklus transaksi.",
				isClean,
				sum.TotalOperations > 0,
				fmt.Sprintf("Pemeriksaan formal model %q atas %d operasi: %d seed linearizable, %d pelanggaran.",
					sum.Model, sum.TotalOperations, sum.SeedsLinearizable, sum.SeedsViolating),
				"Pastikan seluruh skenario pengujian menghasilkan status LINEARIZABLE tanpa counterexample pelanggaran.",
			),
			evalControl(
				FrameworkBIOJK,
				"SEOJK-29/Jejak-Audit",
				"Integritas Bukti Audit Kriptografis (Non-Repudiation)",
				"Artefak hasil pengujian memiliki jejak audit SHA-256 dan tanda tangan kriptografis Ed25519 (in-toto v0.1) yang dapat diverifikasi ulang secara independen.",
				true,
				sum.Attested,
				attestEvidenceString(sum),
				"Tandatangani laporan dengan kunci privat Ed25519 ('strife sign') untuk memenuhi bukti non-repudiation auditor.",
			),
		}
	}
	return nil
}

func evalControl(fw FrameworkID, id, title, req string, safetyOK, coverageOK bool, evidence, gapHint string) ControlResult {
	cr := ControlResult{
		Framework:   fw,
		ControlID:   id,
		Title:       title,
		Requirement: req,
		Evidence:    evidence,
	}
	if !safetyOK {
		cr.Status = StatusFail
		cr.Gap = "Consistency violation or inconclusive verdict detected during test execution."
		return cr
	}
	if !coverageOK {
		cr.Status = StatusPartial
		cr.Gap = gapHint
		return cr
	}
	cr.Status = StatusPass
	return cr
}

func attestEvidenceString(sum EvidenceSummary) string {
	if sum.Attested {
		return fmt.Sprintf("Verified Ed25519 in-toto v0.1 signature by %q (key_id=%s) over fingerprint %s.",
			sum.Signer, sum.KeyID, shortHash(sum.Fingerprint))
	}
	return fmt.Sprintf("SHA-256 fingerprint %s recorded, but no Ed25519 cryptographic signature bundle was supplied.",
		shortHash(sum.Fingerprint))
}

func shortHash(h string) string {
	if len(h) > 16 {
		return h[:16]
	}
	if h == "" {
		return "n/a"
	}
	return h
}

func buildMethodology(sum EvidenceSummary) Methodology {
	tested := []string{
		fmt.Sprintf("Consistency model %q checked across %d operation(s) and %d seed(s) on target %q (%d node(s)).",
			sum.Model, sum.TotalOperations, sum.SeedsTested, sum.Target, sum.Nodes),
		fmt.Sprintf("Fault classes exercised: [%s] (%d fault transition(s)).",
			formatFaultList(sum.FaultKindsExercised), sum.FaultCount),
		"Indeterminate ('info') operations caused by timeouts or connection drops are treated soundly as potentially in-flight from invocation to infinity.",
	}
	if sum.ProbeEvents > 0 {
		tested = append(tested, fmt.Sprintf("Server-side diagnostic probes collected %d kernel/process telemetry event(s) alongside client history.", sum.ProbeEvents))
	}

	var missing []string
	allKinds := nemesis.AllKinds()
	for _, k := range allKinds {
		if !hasAnyFault(sum.FaultKindsExercised, k) {
			missing = append(missing, string(k))
		}
	}
	var notTested []string
	if len(missing) > 0 {
		notTested = append(notTested, fmt.Sprintf("Fault classes not included in this run's schedule: [%s].", strings.Join(missing, ", ")))
	}
	notTested = append(notTested,
		"Byzantine / malicious node behavior, silent disk bit-rot (corrupted sector payload without I/O error), and application business logic outside the tested state machine.",
		"Organizational governance, physical data center security, and personnel policies required for a complete regulatory audit.",
	)

	var limits []string
	switch sum.RunKind {
	case report.KindSimulation, "campaign":
		limits = append(limits,
			"Simulation runs execute inside Strife's deterministic discrete-event simulator: given the same binary, seed, and flags, execution is bit-for-bit reproducible.",
			"Simulation tests the protocol implementation compiled into the simulator, not external OS kernel networking or container configurations.",
		)
	case report.KindLive:
		limits = append(limits,
			"Live runs replay a deterministic fault schedule and per-process operation stream against real cluster nodes, but OS scheduling and TCP/IP interleavings are nondeterministic and captured in history.jsonl.",
			"Passing a live run proves that the recorded history of operations is linearizable; running multiple seeds increases coverage of race windows.",
		)
	default:
		limits = append(limits,
			"Offline history checks verify that a pre-recorded history.jsonl satisfies the consistency model specification.",
		)
	}

	return Methodology{
		WhatIsTested:    tested,
		WhatIsNotTested: notTested,
		ExecutionLimits: limits,
		AuditorDisclaimer: "This dossier provides automated, cryptographically verifiable technical evidence of distributed consistency and fault tolerance. " +
			"Final regulatory compliance determination must be reviewed by a qualified compliance officer or external auditor in conjunction with organizational controls.",
	}
}

// Markdown renders the compliance dossier as a structured Markdown document.
func Markdown(w io.Writer, d *Dossier) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Strife Regulatory Compliance & Resilience Dossier — %s\n\n", d.Summary.Target)
	fmt.Fprintf(&b, "**Overall Control Status:** %s  \n", statusBadge(d.Overall))
	fmt.Fprintf(&b, "**Run Kind:** `%s` · **Model:** `%s` · **Verdict:** `%s`  \n", d.Summary.RunKind, d.Summary.Model, d.Summary.Verdict)
	fmt.Fprintf(&b, "**Dossier SHA-256:** `%s` · **Generated:** `%s`\n\n", d.DossierHash, d.GeneratedAt)

	b.WriteString("## 1. Empirical Evidence Summary\n\n")
	b.WriteString("| Metric | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Target System | `%s` |\n", d.Summary.Target)
	fmt.Fprintf(&b, "| Cluster Nodes | %d |\n", d.Summary.Nodes)
	fmt.Fprintf(&b, "| Seeds Tested | %d (%d linearizable, %d violating) |\n", d.Summary.SeedsTested, d.Summary.SeedsLinearizable, d.Summary.SeedsViolating)
	fmt.Fprintf(&b, "| Client Operations | %d |\n", d.Summary.TotalOperations)
	fmt.Fprintf(&b, "| Fault Classes Exercised | `%s` |\n", formatFaultList(d.Summary.FaultKindsExercised))
	fmt.Fprintf(&b, "| Report / Campaign Fingerprint | `%s` |\n", d.Summary.Fingerprint)
	if d.Summary.HistoryHash != "" {
		fmt.Fprintf(&b, "| History SHA-256 | `%s` |\n", d.Summary.HistoryHash)
	}
	if d.Summary.Attested {
		fmt.Fprintf(&b, "| Cryptographic Attestation | ✅ Verified Ed25519 (`%s`, key `%s`) |\n", d.Summary.Signer, d.Summary.KeyID)
	} else {
		b.WriteString("| Cryptographic Attestation | ⚠️ Unsigned (SHA-256 fingerprint only) |\n")
	}
	b.WriteString("\n")

	b.WriteString("## 2. Regulatory Control Mapping Matrix\n\n")
	b.WriteString("| Framework | Control ID | Title | Status | Technical Evidence & Gap Analysis |\n|---|---|---|---|---|\n")
	for _, c := range d.Controls {
		note := c.Evidence
		if c.Gap != "" {
			note += " **Gap:** " + c.Gap
		}
		fmt.Fprintf(&b, "| `%s` | **%s** | %s | %s | %s |\n",
			strings.ToUpper(string(c.Framework)), c.ControlID, c.Title, statusBadge(c.Status), note)
	}
	b.WriteString("\n")

	b.WriteString("## 3. Methodology, Scope & Technical Boundaries\n\n")
	b.WriteString("### What Is Tested\n\n")
	for _, item := range d.Methodology.WhatIsTested {
		fmt.Fprintf(&b, "- %s\n", item)
	}
	b.WriteString("\n### What Is Not Tested\n\n")
	for _, item := range d.Methodology.WhatIsNotTested {
		fmt.Fprintf(&b, "- %s\n", item)
	}
	b.WriteString("\n### Execution & Reproducibility Boundaries\n\n")
	for _, item := range d.Methodology.ExecutionLimits {
		fmt.Fprintf(&b, "- %s\n", item)
	}
	fmt.Fprintf(&b, "\n> **Auditor Notice:** %s\n\n", d.Methodology.AuditorDisclaimer)
	fmt.Fprintf(&b, "---\nGenerated by Strife %s\n", d.Version)

	_, err := io.WriteString(w, b.String())
	return err
}

func statusBadge(s ControlStatus) string {
	switch s {
	case StatusPass:
		return "✅ PASS"
	case StatusPartial:
		return "⚠️ PARTIAL"
	default:
		return "❌ FAIL"
	}
}

// WriteDir writes dossier.json, dossier.md, and dossier.html into dir.
func WriteDir(dir string, d *Dossier) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	write := func(name string, fn func(f *os.File) error) error {
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			return err
		}
		if err := fn(f); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		written = append(written, p)
		return nil
	}
	steps := []struct {
		name string
		fn   func(f *os.File) error
	}{
		{"dossier.json", func(f *os.File) error {
			enc := json.NewEncoder(f)
			enc.SetIndent("", "  ")
			return enc.Encode(d)
		}},
		{"dossier.md", func(f *os.File) error { return Markdown(f, d) }},
		{"dossier.html", func(f *os.File) error { return HTML(f, d) }},
	}
	for _, s := range steps {
		if err := write(s.name, s.fn); err != nil {
			return written, err
		}
	}
	return written, nil
}
