
# Compliance

The Compliance section provides fleet-wide compliance overview, framework-specific reports, and an executive summary. Available at `/compliance`.

## Hub Page (`/compliance`)

### Compliance Gauge

A large percentage display with a color-coded progress bar:

- Green (> 80%): fleet is healthy
- Yellow (50-80%): fleet has significant drift
- Red (< 50%): fleet is in poor compliance

### Breakdown Cards

Five inline counters showing the distribution:

| Counter | Color | Description |
|---------|-------|-------------|
| Compliant | Green | Envoys with no changes in the last run |
| Drifted | Yellow | Envoys that converged but had changes |
| Failing | Red | Envoys where the last run failed |
| Stale | Yellow | Envoys that have not checked in |
| Unknown | Gray | Envoys with no run history |

### 30-Day Compliance Trend

An SVG sparkline showing daily compliance percentage over the last 30 days. Only displayed when historical data is available in the `compliance_history` table.

### Executive Summary Link

A prominent card linking to the executive summary report (`/compliance/executive`). Designed for C-suite and auditors.

### Framework Grid

A 3-column grid of cards for all 9 compliance frameworks. Each card shows:

- Framework name
- Score percentage (color-coded: green >=95%, yellow >=80%, red <80%)
- Stacked progress bar (green/yellow/red for satisfied/partial/not-satisfied)
- Control counts
- "View Report" link to the per-framework detail page

Framework data is fetched from `GET /api/v1/report/executive` via JavaScript.

### Non-Compliant Envoys Table

A table listing all envoys that are not compliant.

## Framework Report Pages (`/compliance/{framework}`)

Each of the 10 frameworks has its own page:

| Route | Framework |
|-------|-----------|
| `/compliance/compliance` | HIPAA |
| `/compliance/hitrust` | HITRUST CSF |
| `/compliance/soc2` | SOC 2 |
| `/compliance/pci` | PCI DSS v4.0 |
| `/compliance/cis` | CIS Benchmarks |
| `/compliance/nist` | NIST 800-53 |
| `/compliance/iso27001` | ISO 27001 |
| `/compliance/gdpr` | GDPR |
| `/compliance/nerccip` | NERC CIP |
| `/compliance/cyberessentials` | Cyber Essentials+ |

Each page includes:

- Back navigation to the compliance hub
- Download buttons (JSON, HTML, Print)
- Fleet compliance gauge and breakdown cards
- Audit chain status banner
- Controls table with control ID, title, status badge, and evidence
- Fleet status table with per-node details

Report data is fetched from the existing `GET /api/v1/report/{framework}` API endpoint via JavaScript.

## Executive Summary Page (`/compliance/executive`)

A summary-oriented page for non-technical audiences:

- Compliance score with rating (Excellent/Good/Needs Attention/Critical)
- Fleet summary with friendly labels
- Audit chain status banner
- Top 10 issues in plain English (severity-sorted)
- Framework coverage table with stacked bars

Includes download buttons for JSON, HTML, and a print-friendly version.

## Key Actions

- Identify non-compliant envoys at a glance
- Track compliance trends over time via the sparkline
- Generate framework-specific reports for auditors
- Print executive summaries for leadership
- Download reports in JSON or HTML format

## Data Sources

- Compliance counters: FleetIndex (`ComplianceSummary()`)
- Sparkline data: `compliance_history` table (daily snapshots)
- Non-compliant envoy list: FleetIndex, filtered to exclude status "compliant"
- Framework data: `GET /api/v1/report/executive` API (fetched client-side)
- Per-framework reports: `GET /api/v1/report/{framework}` API (fetched client-side)
