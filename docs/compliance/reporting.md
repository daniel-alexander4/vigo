---
title: HIPAA Compliance Reporting
---

# HIPAA Compliance Reporting

Hobnob generates HIPAA compliance reports that map technical controls to 45 CFR 164.312 requirements. Reports include fleet compliance status, audit chain integrity verification, and per-node evidence.

## Report Formats

| Format | Endpoint | Use Case |
|--------|----------|----------|
| JSON | `GET /api/v1/report/compliance` | Programmatic consumption, CI/CD pipelines |
| HTML | `GET /api/v1/report/compliance.html` | Auditor-readable, printable, downloadable |
| OSCAL | `GET /api/v1/export/oscal` | Machine-readable OSCAL Assessment Results v1.1.2 |

## Generating Reports

### CLI

```bash
# JSON report to stdout
hobnobcli report compliance

# HTML report to file
hobnobcli report compliance --format html --output /tmp/compliance-report.html

# JSON report to file
hobnobcli report compliance --format json --output /tmp/compliance.json
```

### REST API

```bash
# JSON report
curl -s https://hobnob:8443/api/v1/report/compliance | jq .

# HTML report (view in browser)
curl -s https://hobnob:8443/api/v1/report/compliance.html > report.html

# HTML report (download with filename)
curl -s "https://hobnob:8443/api/v1/report/compliance.html?download=1" -o report.html

# OSCAL Assessment Results
curl -s https://hobnob:8443/api/v1/export/oscal | jq .
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
| `satisfied` | Control is fully implemented |
| `partial` | Implementation exists but requires per-deployment action (e.g., BAA execution) |
| `not_satisfied` | Control is not met (e.g., broken audit chain) |

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

The HTML report is a self-contained, printable document branded with the Hobnob name. It includes:

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

## Scheduled Reports

For automated compliance snapshots, schedule report generation via cron:

```bash
# Weekly HTML report archived with date stamp
0 2 * * 1 /usr/local/bin/hobnobcli report compliance --format html \
  --output /backup/compliance/hobnob-$(date +\%Y\%m\%d).html
```

## Remediation Integration

When the report shows non-compliant nodes, use the remediation system to automatically fix them:

```bash
# Check what needs remediation
hobnobcli remediation status

# Preview remediation targets
hobnobcli remediation run --dry-run

# Execute remediation (force-push reconvergence)
hobnobcli remediation run
```

See [Remediation Workflow](remediation.md) for details.
