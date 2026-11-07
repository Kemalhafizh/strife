package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// SARIF 2.1.0 schema constants.
const (
	SARIFVersion = "2.1.0"
	SARIFSchema  = "https://json.schemastore.org/sarif-2.1.0.json"
)

type sarifLog struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules,omitempty"`
}

type sarifRule struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	ShortDescription sarifMessage `json:"shortDescription"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID              string            `json:"ruleId"`
	Level               string            `json:"level"`
	Message             sarifMessage      `json:"message"`
	Locations           []sarifLocation   `json:"locations,omitempty"`
	PartialFingerprints map[string]string `json:"partialFingerprints,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation *sarifPhysicalLocation `json:"physicalLocation,omitempty"`
	LogicalLocations []sarifLogicalLocation `json:"logicalLocations,omitempty"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

type sarifLogicalLocation struct {
	Name               string `json:"name"`
	FullyQualifiedName string `json:"fullyQualifiedName"`
	Kind               string `json:"kind"`
}

func classifySARIFRule(c Case) (id, name, desc string) {
	switch c.Class {
	case "dora", "soc2", "iso27001", "bi-ojk":
		ctrlID := c.Class
		title := c.Name
		if rest := strings.TrimSpace(strings.TrimPrefix(c.Name, "["+strings.ToUpper(c.Class)+"]")); rest != c.Name {
			if parts := strings.SplitN(rest, " — ", 2); len(parts) == 2 {
				ctrlID = strings.TrimSpace(parts[0])
				title = strings.TrimSpace(parts[1])
			}
		}
		ruleID := "STRIFE-COMPLIANCE-" + strings.ToUpper(strings.NewReplacer(".", "-", "_", "-").Replace(ctrlID))
		return ruleID, "ComplianceControlGap", title
	}
	combined := c.Message + "\n" + c.Detail
	switch {
	case strings.Contains(combined, "LEDGER-CONSERVATION"):
		return "STRIFE-LEDGER-CONSERVATION", "LedgerConservationViolation", "Double-entry ledger violated conservation of money."
	case strings.Contains(combined, "LEDGER-OVERDRAFT"):
		return "STRIFE-LEDGER-OVERDRAFT", "LedgerOverdraftViolation", "Account balance dropped below zero or suffered overdraft write skew."
	case strings.Contains(combined, "LEDGER-DOUBLE-SPEND"):
		return "STRIFE-LEDGER-DOUBLE-SPEND", "LedgerDoubleSpendViolation", "Duplicate application of an indeterminate transfer across retries."
	case strings.Contains(combined, "LEDGER-UNBALANCED"):
		return "STRIFE-LEDGER-UNBALANCED", "LedgerUnbalancedTransaction", "Double-entry transfer deltas did not sum to zero."
	case strings.Contains(combined, "SETTLEMENT-ATOMICITY"):
		return "STRIFE-SETTLEMENT-ATOMICITY", "SettlementAtomicityViolation", "Committed source ledger settlement missing from downstream clearing log."
	case strings.Contains(combined, "SETTLEMENT-NO-ORPHAN-CREDIT"):
		return "STRIFE-SETTLEMENT-NO-ORPHAN-CREDIT", "SettlementOrphanCreditViolation", "Downstream clearing log contains an orphan settlement ID."
	case strings.Contains(combined, "SETTLEMENT-IDEMPOTENCY"):
		return "STRIFE-SETTLEMENT-IDEMPOTENCY", "SettlementIdempotencyViolation", "Duplicate settlement ID applied to source or clearing log."
	case strings.Contains(combined, "G-realtime"):
		return "STRIFE-TXN-G-REALTIME", "RealTimeSerializationInversion", "Transaction serialization order contradicts real-time order."
	case strings.Contains(combined, "G2-item") || strings.Contains(combined, "G2"):
		return "STRIFE-TXN-G2", "AntiDependencyCycle", "Transactional anti-dependency cycle (write skew / phantom anomaly)."
	case strings.Contains(combined, "G1a") || strings.Contains(combined, "G1b") || strings.Contains(combined, "G1c"):
		return "STRIFE-TXN-G1", "DirtyOrCircularReadAnomaly", "Aborted, intermediate, or circular information flow anomaly."
	case strings.Contains(combined, "G0"):
		return "STRIFE-TXN-G0", "DirtyWriteCycle", "Write-write dependency cycle (dirty write)."
	case strings.Contains(combined, "REGRESSION"):
		return "STRIFE-CAMPAIGN-REGRESSION", "SeedRegression", "Seed that passed in baseline manifest now violates consistency."
	case c.Status == Errored:
		return "STRIFE-INCONCLUSIVE", "InconclusiveOrExecutionError", "Checker search budget exhausted or runtime/transport error occurred."
	default:
		return "STRIFE-LINEARIZABILITY", "LinearizabilityViolation", "Execution history is not linearizable."
	}
}

// WriteSARIF serializes suites as a SARIF 2.1.0 JSON document.
func WriteSARIF(w io.Writer, suites []Suite) error {
	ruleIndex := make(map[string]bool)
	var rules []sarifRule
	results := make([]sarifResult, 0)

	for _, s := range suites {
		for _, c := range s.Cases {
			if c.Status != Failed && c.Status != Errored {
				continue
			}
			ruleID, ruleName, ruleDesc := classifySARIFRule(c)
			if !ruleIndex[ruleID] {
				ruleIndex[ruleID] = true
				rules = append(rules, sarifRule{
					ID:               ruleID,
					Name:             ruleName,
					ShortDescription: sarifMessage{Text: ruleDesc},
				})
			}
			level := "error"
			if c.Status == Errored {
				level = "warning"
			}
			msg := c.Message
			if c.Detail != "" && !strings.Contains(msg, c.Detail) {
				msg = strings.TrimSpace(msg + "\n\n" + c.Detail)
			}
			if msg == "" {
				msg = ruleDesc
			}
			fqn := c.Name
			if c.Class != "" {
				fqn = c.Class + "/" + c.Name
			}
			h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s", ruleID, fqn, c.Message)))
			results = append(results, sarifResult{
				RuleID:  ruleID,
				Level:   level,
				Message: sarifMessage{Text: msg},
				Locations: []sarifLocation{{
					PhysicalLocation: &sarifPhysicalLocation{ArtifactLocation: sarifArtifactLocation{URI: "strife.json"}},
					LogicalLocations: []sarifLogicalLocation{{Name: c.Name, FullyQualifiedName: fqn, Kind: "test"}},
				}},
				PartialFingerprints: map[string]string{"strife/v1": hex.EncodeToString(h[:16])},
			})
		}
	}

	log := sarifLog{
		Version: SARIFVersion,
		Schema:  SARIFSchema,
		Runs: []sarifRun{{
			Tool:    sarifTool{Driver: sarifDriver{Name: "Strife", Version: Version, InformationURI: "https://github.com/kemalhafizh/strife", Rules: rules}},
			Results: results,
		}},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(log)
}
