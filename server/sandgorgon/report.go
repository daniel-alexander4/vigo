// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package sandgorgon

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"time"

	"git.alexander4.io/vigo/server/db"
)

// LifecycleReport is a full audit-trail report for a bare metal asset.
type LifecycleReport struct {
	// Asset identity.
	AssetID      string `json:"asset_id"`
	Hostname     string `json:"hostname"`
	SerialNumber string `json:"serial_number"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	AssetTag     string `json:"asset_tag"`
	RackLocation string `json:"rack_location"`

	// Lifecycle timestamps.
	LifecycleState   string `json:"lifecycle_state"`
	CommissionedAt   string `json:"commissioned_at,omitempty"`
	ProvisionedAt    string `json:"provisioned_at,omitempty"`
	DecommissionedAt string `json:"decommissioned_at,omitempty"`

	// Vigo operational data (populated when envoy is linked).
	EnvoyID           string               `json:"envoy_id,omitempty"`
	ConvergenceStatus string               `json:"convergence_status,omitempty"`
	RiskScore         int                  `json:"risk_score,omitempty"`
	RiskLevel         string               `json:"risk_level,omitempty"`
	RiskFactors       []RiskFactor         `json:"risk_factors,omitempty"`
	ComplianceCoverage []ComplianceCoverage `json:"compliance_coverage,omitempty"`

	// Certificates.
	Certificates []CertificateSummary `json:"certificates,omitempty"`

	// Events.
	Events []EventEntry `json:"events"`

	// Report metadata.
	GeneratedAt string `json:"generated_at"`
	Standard    string `json:"standard"`
	Organization string `json:"organization"`
}

// RiskFactor is a contributing factor to risk score.
type RiskFactor struct {
	Name   string `json:"name"`
	Points int    `json:"points"`
	Detail string `json:"detail"`
}

// ComplianceCoverage is per-framework compliance data.
type ComplianceCoverage struct {
	Framework   string  `json:"framework"`
	CoveragePercent float64 `json:"coverage_percent"`
	Covered     int     `json:"covered"`
	Total       int     `json:"total"`
}

// CertificateSummary is a brief view of a NIST 800-88 certificate.
type CertificateSummary struct {
	ID                 string `json:"id"`
	SanitizationMethod string `json:"sanitization_method"`
	VerificationResult string `json:"verification_result"`
	Technician         string `json:"technician"`
	CompletedAt        string `json:"completed_at"`
}

// EventEntry is a lifecycle event for the report.
type EventEntry struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Detail    string `json:"detail"`
}

// LifecycleSummary is the JSON structure stored in sandgorgon_certificates.lifecycle_summary.
type LifecycleSummary struct {
	EnvoyID            string               `json:"envoy_id,omitempty"`
	ConvergenceStatus  string               `json:"convergence_status,omitempty"`
	RiskScore          int                  `json:"risk_score,omitempty"`
	RiskLevel          string               `json:"risk_level,omitempty"`
	ComplianceCoverage []ComplianceCoverage `json:"compliance_coverage,omitempty"`
}

// LifecycleDataSource provides Vigo operational data for lifecycle reports.
// This interface decouples sandgorgon from direct fleet/risk/config imports.
type LifecycleDataSource interface {
	// GetEnvoyRisk returns risk score and factors for an envoy.
	GetEnvoyRisk(envoyID string) (score int, level string, factors []RiskFactor, ok bool)
	// GetEnvoyConvergenceStatus returns the convergence status.
	GetEnvoyConvergenceStatus(envoyID string) (status string, ok bool)
	// GetEnvoyComplianceCoverage returns per-framework compliance coverage.
	GetEnvoyComplianceCoverage(hostname string) []ComplianceCoverage
}

// SetLifecycleDataSource enables operational data in reports.
func (m *Manager) SetLifecycleDataSource(ds LifecycleDataSource) {
	m.lifecycleDS = ds
}

// BuildLifecycleReport constructs a full lifecycle report for an asset.
func (m *Manager) BuildLifecycleReport(ctx context.Context, assetID, org string) (*LifecycleReport, error) {
	asset, err := m.queries.GetSandgorgonAsset(ctx, assetID)
	if err != nil {
		return nil, fmt.Errorf("asset not found: %w", err)
	}

	report := &LifecycleReport{
		AssetID:        asset.ID,
		Hostname:       asset.Hostname,
		SerialNumber:   asset.SerialNumber,
		Manufacturer:   asset.Manufacturer,
		Model:          asset.Model,
		AssetTag:       asset.AssetTag,
		RackLocation:   asset.RackLocation,
		LifecycleState: asset.LifecycleState,
		GeneratedAt:    time.Now().Format(time.RFC3339),
		Standard:       "NIST SP 800-88 Rev. 2",
		Organization:   org,
	}

	if asset.CommissionedAt.Valid {
		report.CommissionedAt = asset.CommissionedAt.Time.Local().Format(time.RFC3339)
	}
	if asset.ProvisionedAt.Valid {
		report.ProvisionedAt = asset.ProvisionedAt.Time.Local().Format(time.RFC3339)
	}
	if asset.DecommissionedAt.Valid {
		report.DecommissionedAt = asset.DecommissionedAt.Time.Local().Format(time.RFC3339)
	}

	// Pull Vigo operational data if envoy is linked.
	if asset.EnvoyID.Valid && m.lifecycleDS != nil {
		eid := asset.EnvoyID.String
		report.EnvoyID = eid

		if status, ok := m.lifecycleDS.GetEnvoyConvergenceStatus(eid); ok {
			report.ConvergenceStatus = status
		}
		if score, level, factors, ok := m.lifecycleDS.GetEnvoyRisk(eid); ok {
			report.RiskScore = score
			report.RiskLevel = level
			report.RiskFactors = factors
		}
		report.ComplianceCoverage = m.lifecycleDS.GetEnvoyComplianceCoverage(asset.Hostname)
	}

	// Certificates.
	certs, _ := m.queries.ListSandgorgonCertificatesByAsset(ctx, assetID)
	for _, c := range certs {
		completed := ""
		if c.CompletedAt.Valid {
			completed = c.CompletedAt.Time.Local().Format(time.RFC3339)
		}
		report.Certificates = append(report.Certificates, CertificateSummary{
			ID:                 c.ID,
			SanitizationMethod: c.SanitizationMethod,
			VerificationResult: c.VerificationResult,
			Technician:         c.Technician,
			CompletedAt:        completed,
		})
	}

	// Events.
	events, _ := m.queries.ListSandgorgonEventsByAsset(ctx, db.ListSandgorgonEventsByAssetParams{
		AssetID: assetID,
		Limit:   200,
	})
	for _, e := range events {
		report.Events = append(report.Events, EventEntry{
			Timestamp: e.CreatedAt.Local().Format(time.RFC3339),
			Type:      e.EventType,
			Detail:    e.Detail,
		})
	}

	return report, nil
}

// BuildLifecycleSummaryJSON generates the JSON to store in a certificate's lifecycle_summary field.
func (m *Manager) BuildLifecycleSummaryJSON(asset *db.SandgorgonAsset) string {
	summary := LifecycleSummary{}

	if asset.EnvoyID.Valid && m.lifecycleDS != nil {
		eid := asset.EnvoyID.String
		summary.EnvoyID = eid

		if status, ok := m.lifecycleDS.GetEnvoyConvergenceStatus(eid); ok {
			summary.ConvergenceStatus = status
		}
		if score, level, _, ok := m.lifecycleDS.GetEnvoyRisk(eid); ok {
			summary.RiskScore = score
			summary.RiskLevel = level
		}
		summary.ComplianceCoverage = m.lifecycleDS.GetEnvoyComplianceCoverage(asset.Hostname)
	}

	data, err := json.Marshal(summary)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// RenderLifecycleReportHTML writes a self-contained, print-friendly HTML lifecycle report.
func RenderLifecycleReportHTML(w io.Writer, report *LifecycleReport) error {
	return lifecycleReportTmpl.Execute(w, report)
}

var lifecycleReportTmpl = template.Must(template.New("lifecycle-report").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Lifecycle Report — {{.Hostname}}</title>
<style>
  @media print { body { margin: 0; } .no-print { display: none; } }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; max-width: 900px; margin: 40px auto; color: #1e1e2e; line-height: 1.5; }
  h1 { text-align: center; font-size: 1.5em; margin-bottom: 4px; }
  .subtitle { text-align: center; color: #585b70; margin-bottom: 32px; }
  h2 { font-size: 1.1em; border-bottom: 2px solid #89b4fa; padding-bottom: 4px; margin-top: 28px; }
  table { width: 100%; border-collapse: collapse; margin-bottom: 16px; }
  th, td { text-align: left; padding: 6px 10px; border-bottom: 1px solid #cdd6f4; }
  th { width: 180px; color: #585b70; font-weight: 500; }
  .risk-score { font-size: 1.4em; font-weight: bold; }
  .risk-low { color: #a6e3a1; } .risk-medium { color: #f9e2af; }
  .risk-high { color: #fab387; } .risk-critical { color: #f38ba8; }
  .compliance-bar { background: #313244; border-radius: 4px; height: 16px; position: relative; }
  .compliance-fill { background: #a6e3a1; border-radius: 4px; height: 100%; }
  .event-row td:first-child { white-space: nowrap; color: #585b70; font-size: 0.9em; }
  .footer { text-align: center; color: #585b70; font-size: 0.85em; margin-top: 40px; border-top: 1px solid #cdd6f4; padding-top: 12px; }
</style>
</head>
<body>

<h1>Bare Metal Lifecycle Report</h1>
<p class="subtitle">{{.Hostname}} — {{.Standard}}</p>

<h2>Asset Identification</h2>
<table>
  <tr><th>Hostname</th><td>{{.Hostname}}</td></tr>
  <tr><th>Serial Number</th><td>{{.SerialNumber}}</td></tr>
  <tr><th>Manufacturer</th><td>{{.Manufacturer}}</td></tr>
  <tr><th>Model</th><td>{{.Model}}</td></tr>
  <tr><th>Asset Tag</th><td>{{.AssetTag}}</td></tr>
  <tr><th>Rack Location</th><td>{{.RackLocation}}</td></tr>
  <tr><th>Lifecycle State</th><td>{{.LifecycleState}}</td></tr>
</table>

<h2>Lifecycle Timeline</h2>
<table>
  {{if .CommissionedAt}}<tr><th>Commissioned</th><td>{{.CommissionedAt}}</td></tr>{{end}}
  {{if .ProvisionedAt}}<tr><th>Provisioned</th><td>{{.ProvisionedAt}}</td></tr>{{end}}
  {{if .DecommissionedAt}}<tr><th>Decommissioned</th><td>{{.DecommissionedAt}}</td></tr>{{end}}
</table>

{{if .EnvoyID}}
<h2>Vigo Operational Data</h2>
<table>
  <tr><th>Envoy ID</th><td>{{.EnvoyID}}</td></tr>
  <tr><th>Convergence</th><td>{{.ConvergenceStatus}}</td></tr>
  <tr><th>Risk Score</th><td><span class="risk-score risk-{{.RiskLevel}}">{{.RiskScore}}</span> ({{.RiskLevel}})</td></tr>
</table>

{{if .RiskFactors}}
<h3>Risk Factors</h3>
<table>
  <tr><th>Factor</th><th>Points</th><th>Detail</th></tr>
  {{range .RiskFactors}}
  <tr><td>{{.Name}}</td><td>+{{.Points}}</td><td>{{.Detail}}</td></tr>
  {{end}}
</table>
{{end}}

{{if .ComplianceCoverage}}
<h3>Compliance Coverage</h3>
<table>
  <tr><th>Framework</th><th>Coverage</th><th>Controls</th></tr>
  {{range .ComplianceCoverage}}
  <tr>
    <td>{{.Framework}}</td>
    <td>
      <div class="compliance-bar" style="width:200px;">
        <div class="compliance-fill" style="width:{{printf "%.0f" .CoveragePercent}}%;"></div>
      </div>
      {{printf "%.0f" .CoveragePercent}}%
    </td>
    <td>{{.Covered}}/{{.Total}}</td>
  </tr>
  {{end}}
</table>
{{end}}
{{end}}

{{if .Certificates}}
<h2>Sanitization Certificates</h2>
<table>
  <tr><th>Certificate ID</th><th>Method</th><th>Verification</th><th>Technician</th><th>Date</th></tr>
  {{range .Certificates}}
  <tr>
    <td style="font-family:monospace;font-size:0.85em;">{{.ID}}</td>
    <td>{{.SanitizationMethod}}</td>
    <td>{{.VerificationResult}}</td>
    <td>{{.Technician}}</td>
    <td>{{.CompletedAt}}</td>
  </tr>
  {{end}}
</table>
{{end}}

{{if .Events}}
<h2>Lifecycle Events</h2>
<table>
  {{range .Events}}
  <tr class="event-row">
    <td>{{.Timestamp}}</td>
    <td><strong>{{.Type}}</strong></td>
    <td>{{.Detail}}</td>
  </tr>
  {{end}}
</table>
{{end}}

<div class="footer">
  <p>Generated: {{.GeneratedAt}} | {{.Organization}}</p>
  <p>This report documents the complete lifecycle of the asset from commissioning through decommission.<br>
  Compliance and risk data are sourced from Vigo at the time of report generation.</p>
</div>

</body>
</html>`))
