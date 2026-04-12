
# Compliance

The Compliance section provides fleet-wide compliance overview, framework-specific reports, and an executive summary. Available at `/compliance`.

## Hub Page (`/compliance`)

### Compliance Gauge

A large percentage display with a segmented progress bar showing control coverage. The gauge includes explanatory text ("N% of M controls satisfied across all active frameworks") and a color legend for the bar segments:

- **Green**: Satisfied controls
- **Blue**: Waived controls (if any)
- **Yellow**: Partially satisfied controls
- **Red**: Not met controls

The progress bar is segmented proportionally so operators can see the distribution at a glance.

### Breakdown Cards

Five inline counters showing the distribution:

| Counter | Color | Description |
|---------|-------|-------------|
| Satisfied | Green | Controls fully satisfied by module enforcement |
| Waived | Blue | Controls covered by active waivers |
| Partial | Yellow | Controls partially satisfied |
| Not Met | Red | Controls not satisfied |
| Total | Gray | Total controls across all active frameworks |

### 30-Day Compliance Trend

An SVG sparkline showing daily compliance percentage over the last 30 days. Only displayed when historical data is available in the `compliance_history` table.

### Executive Summary Link

A prominent card linking to the executive summary report (`/compliance/executive`). Designed for C-suite and auditors.

### Framework Grid

A 3-column grid of cards for all 9 compliance frameworks. Each card shows:

- Framework name
- Score percentage (HSL spectrum color — smoothly transitions from green at 100% to red at 0%)
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
- Fleet status table with per-envoy details
- **Viz View** button (`/compliance/{framework}/viz`) — switches to a visual view with a fleet coverage heatmap (envoys × controls, color-coded by coverage threshold) and a satisfied/partial/not_satisfied donut chart

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
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
