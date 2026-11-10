// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package sandgorgon

import (
	"fmt"
	"html/template"
	"io"
	"time"

	"git.alexander4.io/vigo/server/db"
)

// CertificateData contains all fields for a NIST 800-88 Rev. 2 certificate.
type CertificateData struct {
	CertificateID           string `json:"certificate_id"`
	Hostname                string `json:"hostname"`
	SerialNumber            string `json:"serial_number"`
	Model                   string `json:"model"`
	AssetTag                string `json:"asset_tag"`
	SanitizationMethod      string `json:"sanitization_method"`
	MediaType               string `json:"media_type"`
	VerificationMethod      string `json:"verification_method"`
	VerificationResult      string `json:"verification_result"`
	VerificationLimitations string `json:"verification_limitations,omitempty"`
	Technician              string `json:"technician"`
	StartedAt               string `json:"started_at"`
	CompletedAt             string `json:"completed_at"`
	IssuedAt                string `json:"issued_at"`
	Standard                string `json:"standard"`
	Organization            string `json:"organization"`
}

// CertificateFromDB converts a database certificate record to display data.
func CertificateFromDB(cert db.SandgorgonCertificate, org string) CertificateData {
	completed := ""
	if cert.CompletedAt.Valid {
		completed = cert.CompletedAt.Time.Local().Format(time.RFC3339)
	}
	return CertificateData{
		CertificateID:           cert.ID,
		Hostname:                cert.Hostname,
		SerialNumber:            cert.SerialNumber,
		Model:                   cert.Model,
		AssetTag:                cert.AssetTag,
		SanitizationMethod:      cert.SanitizationMethod,
		MediaType:               cert.MediaType,
		VerificationMethod:      cert.VerificationMethod,
		VerificationResult:      cert.VerificationResult,
		VerificationLimitations: cert.VerificationLimitations,
		Technician:              cert.Technician,
		StartedAt:               cert.StartedAt.Local().Format(time.RFC3339),
		CompletedAt:             completed,
		IssuedAt:                cert.CreatedAt.Local().Format(time.RFC3339),
		Standard:                "NIST SP 800-88 Rev. 2",
		Organization:            org,
	}
}

// RenderCertificateHTML writes a self-contained, print-friendly HTML certificate.
func RenderCertificateHTML(w io.Writer, data CertificateData) error {
	return certTmpl.Execute(w, data)
}

var certTmpl = template.Must(template.New("cert").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>NIST 800-88 Media Sanitization Certificate</title>
<style>
  @media print { body { margin: 0; } .no-print { display: none; } }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; max-width: 800px; margin: 40px auto; color: #1e1e2e; line-height: 1.5; }
  h1 { text-align: center; font-size: 1.4em; margin-bottom: 4px; }
  .subtitle { text-align: center; color: #585b70; margin-bottom: 32px; }
  table { width: 100%; border-collapse: collapse; margin-bottom: 24px; }
  th, td { text-align: left; padding: 8px 12px; border-bottom: 1px solid #cdd6f4; }
  th { width: 220px; color: #585b70; font-weight: 500; }
  .section { margin-bottom: 24px; }
  .section-title { font-weight: 600; font-size: 1.1em; margin-bottom: 8px; border-bottom: 2px solid #89b4fa; padding-bottom: 4px; }
  .limitations { background: #f5e6d0; padding: 12px; border-radius: 6px; border-left: 4px solid #fab387; }
  .footer { text-align: center; color: #585b70; font-size: 0.85em; margin-top: 40px; border-top: 1px solid #cdd6f4; padding-top: 12px; }
  .cert-id { font-family: monospace; font-size: 0.9em; }
</style>
</head>
<body>

<h1>Media Sanitization Certificate</h1>
<p class="subtitle">{{.Standard}}</p>

<div class="section">
  <div class="section-title">Asset Identification</div>
  <table>
    <tr><th>Hostname</th><td>{{.Hostname}}</td></tr>
    <tr><th>Serial Number</th><td>{{.SerialNumber}}</td></tr>
    <tr><th>Model</th><td>{{.Model}}</td></tr>
    <tr><th>Asset Tag</th><td>{{.AssetTag}}</td></tr>
    <tr><th>Media Type</th><td>{{.MediaType}}</td></tr>
  </table>
</div>

<div class="section">
  <div class="section-title">Sanitization</div>
  <table>
    <tr><th>Method</th><td>{{.SanitizationMethod}}</td></tr>
    <tr><th>Started</th><td>{{.StartedAt}}</td></tr>
    <tr><th>Completed</th><td>{{.CompletedAt}}</td></tr>
    <tr><th>Technician</th><td>{{.Technician}}</td></tr>
  </table>
</div>

<div class="section">
  <div class="section-title">Verification</div>
  <table>
    <tr><th>Verification Method</th><td>{{.VerificationMethod}}</td></tr>
    <tr><th>Result</th><td>{{.VerificationResult}}</td></tr>
  </table>
  {{if .VerificationLimitations}}
  <div class="limitations">
    <strong>Limitations:</strong> {{.VerificationLimitations}}
  </div>
  {{end}}
</div>

<div class="footer">
  <p>Certificate ID: <span class="cert-id">{{.CertificateID}}</span></p>
  <p>Issued: {{.IssuedAt}} | {{.Organization}}</p>
  <p>This certificate was generated in accordance with {{.Standard}} guidelines.<br>
  It documents the sanitization method applied and the verification method used.<br>
  Verification limitations, where applicable, are stated above.</p>
</div>

</body>
</html>`))

// FormatCertificateText returns a plain-text certificate for CLI output.
func FormatCertificateText(data CertificateData) string {
	s := fmt.Sprintf(`NIST SP 800-88 Rev. 2 — Media Sanitization Certificate
======================================================

Asset Identification
  Hostname:       %s
  Serial Number:  %s
  Model:          %s
  Asset Tag:      %s
  Media Type:     %s

Sanitization
  Method:         %s
  Started:        %s
  Completed:      %s
  Technician:     %s

Verification
  Method:         %s
  Result:         %s`,
		data.Hostname, data.SerialNumber, data.Model, data.AssetTag, data.MediaType,
		data.SanitizationMethod, data.StartedAt, data.CompletedAt, data.Technician,
		data.VerificationMethod, data.VerificationResult)

	if data.VerificationLimitations != "" {
		s += fmt.Sprintf("\n  Limitations:    %s", data.VerificationLimitations)
	}

	s += fmt.Sprintf("\n\nCertificate ID: %s\nIssued: %s | %s\n",
		data.CertificateID, data.IssuedAt, data.Organization)

	return s
}
