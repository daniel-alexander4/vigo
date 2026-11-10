// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grc

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Adapter transforms an EvidencePayload into a platform-specific JSON body.
type Adapter interface {
	Format(payload *EvidencePayload) ([]byte, error)
}

// GetAdapter returns the adapter for a named GRC platform.
// Falls back to the generic adapter if the platform isn't recognized.
func GetAdapter(name string) Adapter {
	switch strings.ToLower(name) {
	case "vanta":
		return &vantaAdapter{}
	case "drata":
		return &drataAdapter{}
	case "secureframe":
		return &secureframeAdapter{}
	case "laika":
		return &laikaAdapter{}
	case "thoropass":
		return &thoropassAdapter{}
	case "servicenow":
		return &servicenowGRCAdapter{}
	default:
		return &genericAdapter{}
	}
}

// statusWaived is the EvidenceControl.Status buildPayload gives a control
// every active envoy has waived (pusher.go).
const statusWaived = "waived"

// waivedNote prefixes the evidence of a waived control sent to a platform
// whose status vocabulary has no waived/not-applicable state, so the control
// is reported as passing but the record says why.
func waivedNote(evidence string) string {
	return "Waived (accepted exception, not enforced): " + evidence
}

// --- Generic (sends the payload as-is) ---

type genericAdapter struct{}

func (g *genericAdapter) Format(p *EvidencePayload) ([]byte, error) {
	return json.Marshal(p)
}

// --- Vanta ---
// Vanta expects evidence as individual "test" results.

type vantaAdapter struct{}

type vantaPayload struct {
	TestResults []vantaTest `json:"test_results"`
}

type vantaTest struct {
	ExternalID  string `json:"external_id"`
	DisplayName string `json:"display_name"`
	Result      string `json:"result"` // "PASS" or "FAIL"
	Description string `json:"description"`
	Timestamp   string `json:"timestamp"`
}

func (v *vantaAdapter) Format(p *EvidencePayload) ([]byte, error) {
	var tests []vantaTest
	for _, fw := range p.Frameworks {
		for _, c := range fw.Controls {
			result, desc := "FAIL", c.Evidence
			switch c.Status {
			case "satisfied":
				result = "PASS"
			case statusWaived:
				// PASS/FAIL only: a waiver is an accepted exception, not a failure.
				result, desc = "PASS", waivedNote(c.Evidence)
			}
			tests = append(tests, vantaTest{
				ExternalID:  fmt.Sprintf("vigo-%s-%s", fw.ID, c.ID),
				DisplayName: fmt.Sprintf("[%s] %s", fw.Name, c.Title),
				Result:      result,
				Description: desc,
				Timestamp:   p.Timestamp.Format(time.RFC3339),
			})
		}
	}
	return json.Marshal(vantaPayload{TestResults: tests})
}

// --- Drata ---
// Drata expects evidence objects linked to control IDs.

type drataAdapter struct{}

type drataPayload struct {
	Evidence []drataEvidence `json:"evidence"`
}

type drataEvidence struct {
	ControlID   string `json:"control_id"`
	Status      string `json:"status"` // "passing", "failing", "not_applicable"
	Evidence    string `json:"evidence"`
	Source      string `json:"source"`
	CollectedAt string `json:"collected_at"`
}

func (d *drataAdapter) Format(p *EvidencePayload) ([]byte, error) {
	var evidence []drataEvidence
	for _, fw := range p.Frameworks {
		for _, c := range fw.Controls {
			status := "failing"
			switch c.Status {
			case "satisfied":
				status = "passing"
			case "partial":
				status = "partially_passing"
			case statusWaived:
				status = "not_applicable"
			}
			evidence = append(evidence, drataEvidence{
				ControlID:   fmt.Sprintf("%s-%s", fw.ID, c.ID),
				Status:      status,
				Evidence:    c.Evidence,
				Source:      "vigo",
				CollectedAt: p.Timestamp.Format(time.RFC3339),
			})
		}
	}
	return json.Marshal(drataPayload{Evidence: evidence})
}

// --- Secureframe ---
// Secureframe expects evidence items with control mapping.

type secureframeAdapter struct{}

type secureframePayload struct {
	Items []secureframeItem `json:"items"`
}

type secureframeItem struct {
	Name        string `json:"name"`
	Status      string `json:"status"` // "pass", "fail", "warning"
	ControlRef  string `json:"control_ref"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Timestamp   string `json:"timestamp"`
}

func (s *secureframeAdapter) Format(p *EvidencePayload) ([]byte, error) {
	var items []secureframeItem
	for _, fw := range p.Frameworks {
		for _, c := range fw.Controls {
			status, desc := "fail", c.Evidence
			switch c.Status {
			case "satisfied":
				status = "pass"
			case "partial":
				status = "warning"
			case statusWaived:
				status, desc = "pass", waivedNote(c.Evidence)
			}
			items = append(items, secureframeItem{
				Name:        fmt.Sprintf("[%s] %s", fw.Name, c.Title),
				Status:      status,
				ControlRef:  c.ID,
				Description: desc,
				Source:      "vigo",
				Timestamp:   p.Timestamp.Format(time.RFC3339),
			})
		}
	}
	return json.Marshal(secureframePayload{Items: items})
}

// --- Laika ---
// Similar to Vanta — evidence with test name and pass/fail.

type laikaAdapter struct{}

func (l *laikaAdapter) Format(p *EvidencePayload) ([]byte, error) {
	// Laika uses a similar format to Vanta.
	v := &vantaAdapter{}
	return v.Format(p)
}

// --- Thoropass ---
// Evidence artifacts with control mapping.

type thoropassAdapter struct{}

func (t *thoropassAdapter) Format(p *EvidencePayload) ([]byte, error) {
	// Thoropass uses a similar format to Secureframe.
	s := &secureframeAdapter{}
	return s.Format(p)
}

// --- ServiceNow GRC ---
// Creates/updates policy compliance records.

type servicenowGRCAdapter struct{}

type servicenowGRCPayload struct {
	Records []servicenowRecord `json:"records"`
}

type servicenowRecord struct {
	PolicyID   string `json:"policy_id"`
	ControlID  string `json:"control_id"`
	Status     string `json:"status"` // "compliant", "non_compliant", "partially_compliant"
	Evidence   string `json:"evidence"`
	AssessedAt string `json:"assessed_at"`
	Source     string `json:"source"`
}

func (s *servicenowGRCAdapter) Format(p *EvidencePayload) ([]byte, error) {
	var records []servicenowRecord
	for _, fw := range p.Frameworks {
		for _, c := range fw.Controls {
			status, ev := "non_compliant", c.Evidence
			switch c.Status {
			case "satisfied":
				status = "compliant"
			case "partial":
				status = "partially_compliant"
			case statusWaived:
				status, ev = "compliant", waivedNote(c.Evidence)
			}
			records = append(records, servicenowRecord{
				PolicyID:   fw.ID,
				ControlID:  c.ID,
				Status:     status,
				Evidence:   ev,
				AssessedAt: p.Timestamp.Format(time.RFC3339),
				Source:     "vigo",
			})
		}
	}
	return json.Marshal(servicenowGRCPayload{Records: records})
}
