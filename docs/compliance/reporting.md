
---
title: Compliance Reporting
---

# Compliance Reporting

Vigo generates compliance reports mapped to 18 security frameworks, plus an executive summary for non-technical audiences. Reports include fleet compliance status, audit chain integrity verification, and per-node evidence.

## Report Formats

| Framework | Format | Endpoint | Use Case |
|-----------|--------|----------|----------|
| HIPAA | JSON | `GET /api/v1/report/compliance` | Programmatic consumption |
| HIPAA | HTML | `GET /api/v1/report/compliance.html` | Auditor-readable, printable |
| HITRUST CSF | JSON | `GET /api/v1/report/hitrust` | Programmatic consumption |
| HITRUST CSF | HTML | `GET /api/v1/report/hitrust.html` | Auditor-readable, printable |
| Cyber Essentials+ | JSON | `GET /api/v1/report/cyberessentials` | Programmatic consumption |
| Cyber Essentials+ | HTML | `GET /api/v1/report/cyberessentials.html` | Auditor-readable, printable |
| Executive Summary | JSON | `GET /api/v1/report/executive` | C-suite / auditor overview |
| Executive Summary | HTML | `GET /api/v1/report/executive.html` | Print-friendly PDF via Ctrl+P |
| OSCAL | JSON | `GET /api/v1/export/oscal` | Machine-readable OSCAL v1.1.2 |

## Generating Reports

### CLI

```bash
# JSON report to stdout
vigogo report compliance

# HTML report to file
vigogo report compliance --format html --output /tmp/compliance-report.html

# JSON report to file
vigogo report compliance --format json --output /tmp/compliance.json
```

### REST API

```bash
# JSON report
curl -s https://vigo:8443/api/v1/report/compliance | jq .

# HTML report (view in browser)
curl -s https://vigo:8443/api/v1/report/compliance.html > report.html

# HTML report (download with filename)
curl -s "https://vigo:8443/api/v1/report/compliance.html?download=1" -o report.html

# OSCAL Assessment Results
curl -s https://vigo:8443/api/v1/export/oscal | jq .
```

## Report Contents

### Summary Section

- **Compliance percentage** — fleet-wide compliance rate
- **Node counts** — total, compliant, errors, recurring drift, lost contact
- **Audit chain status** — SHA-256 hash chain integrity (valid/broken)

### HIPAA Controls

Each control is mapped to its implementation status:

| Status | Meaning |
|--------|---------|
| `satisfied` | Control is fully implemented by Vigo |
| `satisfied` (with "ENFORCED when modules assigned") | Vigo enforces the control, but coverage depends on which modules the operator assigns to each node |
| `partial` | Vigo provides supporting infrastructure but the control requires organizational action (e.g., incident response plans, business continuity planning, security policies) |
| `partial` (with "MONITORED") | Vigo monitors the relevant services but cannot perform the verification itself (e.g., DR testing, WORM storage compliance) |
| `not_satisfied` | Control is outside Vigo's scope (e.g., RNG certification by accredited labs, electronic signatures, proof of reserves) |

Controls covered:

| Control | Title |
|---------|-------|
| 164.312(a)(1) | Access Control |
| 164.312(a)(2)(i) | Unique User Identification |
| 164.312(a)(2)(ii) | Emergency Access Procedure |
| 164.312(a)(2)(iii) | Automatic Logoff |
| 164.312(a)(2)(iv) | Encryption and Decryption |
| 164.312(b) | Audit Controls |
| 164.312(c)(1) | Integrity |
| 164.312(d) | Person or Entity Authentication |
| 164.312(e)(1) | Transmission Security |
| 164.312(e)(2)(ii) | Encryption (Transmission) |
| 164.310(d)(2)(i) | Disposal |
| 164.308(a)(5)(ii)(D) | Password Management |
| 164.308(a)(6) | Security Incident Procedures |
| 164.308(b) | Business Associate Contracts |

### Fleet Status Table

Per-node breakdown with hostname, compliance status, OS, IP, and last seen timestamp.

### Audit Chain Verification

The report automatically verifies the tamper-evident audit chain and includes the result. A broken chain marks the audit controls finding as `not_satisfied`.

## HTML Report

The HTML report is a self-contained, printable document branded with the Vigo name. It includes:

- Summary cards with color-coded compliance percentage
- Audit chain integrity banner
- HIPAA controls table with status badges
- Fleet status table with per-node compliance

The HTML is designed for:
- **Printing** — clean layout, print-optimized CSS
- **Archiving** — self-contained, no external dependencies
- **Auditor review** — clear, professional formatting

## OSCAL Export

The OSCAL (Open Security Controls Assessment Language) export produces a valid NIST OSCAL Assessment Results v1.1.2 document with:

- 13 findings mapped to HIPAA Security Rule controls
- Per-finding status (`satisfied`, `partial`, `not_satisfied`)
- Evidence descriptions for each control
- Standard UUID identifiers

This format is consumable by GRC tools (Archer, ServiceNow GRC, RegScale) and federal compliance platforms.

## Executive Summary Report

The executive summary is designed for business owners, C-suite, and auditors who need a high-level compliance overview without per-node technical detail.

### Contents

- **Compliance Score** — fleet-wide percentage with rating (Excellent/Good/Needs Attention/Critical)
- **30-Day Trend** — daily compliance history as an inline SVG area chart
- **Framework Coverage** — all 18 frameworks with satisfied/partial/not-satisfied counts and stacked bar charts
- **Fleet Summary** — friendly labels: Fully Compliant, Configuration Changes Detected, Errors, Unreachable, Pending Data
- **Top Issues** — max 10, severity-sorted, plain-English descriptions
- **Audit Chain** — pass/fail integrity banner

### CLI

```bash
# JSON to stdout
vigogo report executive

# HTML report to file (print as PDF via Ctrl+P)
vigogo report executive --format html --output /tmp/executive-summary.html

# JSON to file
vigogo report executive --format json --output /tmp/executive.json
```

### REST API

```bash
# JSON report
curl -s https://vigo:8443/api/v1/report/executive | jq .

# HTML report
curl -s https://vigo:8443/api/v1/report/executive.html > executive.html

# Download with filename
curl -s "https://vigo:8443/api/v1/report/executive.html?download=1" -o executive.html
```

### Print to PDF

The HTML report includes `@media print` CSS for clean PDF output. Open the HTML file in a browser and press Ctrl+P (Cmd+P on macOS) to print or save as PDF.

## Scheduled Reports

For automated compliance snapshots, schedule report generation via cron:

```bash
# Weekly HTML report archived with date stamp
0 2 * * 1 /usr/local/bin/vigogo report compliance --format html \
  --output /backup/compliance/vigo-$(date +\%Y\%m\%d).html
```

## Remediation Integration

When the report shows non-compliant nodes, use the remediation system to automatically fix them:

```bash
# Check what needs remediation
vigogo remediation status

# Preview remediation targets
vigogo remediation run --dry-run

# Execute remediation (force-convergence)
vigogo remediation run
```

See [Remediation Workflow](remediation.md) for details.
