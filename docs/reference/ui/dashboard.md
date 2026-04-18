
# Dashboard

The dashboard is the landing page at `/`. It compresses fleet state into a
single glanceable grid of cards — convergence, security, compliance, risk,
plus an Attention column when there is something to act on. Every card is
clickable and links to the detail page for that dimension.

## Sidebar

The sidebar navigation appears on all pages. It shows the Vigo logo (or
custom org logo if branding is configured), the org name (if set), and the
server version below the logo. The sidebar can be collapsed via the toggle
button.

## Layout

### Version Info Card

A card at the top of the dashboard showing the current server version,
agent version, and available agent platforms (OS/architecture combinations).
Static — does not auto-refresh.

### License Capacity Banner

Shown above the card grid when the fleet is at ≥90% of the licensed node
count (orange) or at 100% (red). Renders a link to `/license`.

### Card Grid

The grid uses `auto-fit minmax(280px, 1fr)` and reflows from 1 to 5 columns
across viewport widths. The full cards fragment auto-refreshes every 30s
via HTMX (`hx-get="/ui/fragments/dashboard-stats"`).

Each card is wrapped in a block-level `<a>` so the whole card is clickable.
Hover lifts the card with a subtle shadow.

| Card | Destination | What it shows |
|------|-------------|---------------|
| **Attention** | per-row drill-down (see below) | Actionable rows: failed runs, offline envoys, relapsed, diverged, critical risk. Rendered as a compact horizontal strip above the main stat columns, sized to its own content. Only shown when one or more counts > 0. |
| **Fleet Health & Module Convergence** | `/envoys` | Total envoys, active (1h), and a combined convergence card showing the percent headline with an "X of Y converged" label. 30-day convergence % sparkline below the stat cards (fixed 0–100 axis). |
| **Security Posture** | `/ask` | Unique CVEs, avg Lynis hardening score, rootkit warnings. |
| **Standards Compliance** | `/compliance` | Partial + Not Met control counts with a combined coverage card at the bottom showing the percent headline with an "X of Y covered" label. 30-day coverage % sparkline (fixed 0–100 axis). |
| **Risk Posture** | `/risk` | Avg risk score, critical / high / medium counts. 30-day avg risk score sparkline (fixed 0–100 axis). |

The Attention column is list-style, not cards: one row per actionable
category, each a link to the pre-filtered list page:

| Row | Destination | Level |
|-----|-------------|-------|
| Failed runs | `/envoys?filter=failed` | critical |
| Offline envoys | `/envoys?filter=offline` | warning |
| Relapsed | `/envoys?filter=relapsed` | warning |
| Diverged | `/envoys?filter=diverged` | warning |
| Critical risk | `/risk` | critical |

Rows with a zero count are omitted. When all five are zero, the Attention
column is hidden entirely.

### Onboarding Card

When no envoys are enrolled, the dashboard replaces the card grid with a
welcome card containing numbered getting-started steps and a link to the
documentation. The card disappears once the first envoy enrolls.

## Drill-Down Map

The dashboard is a glance surface — detail lives elsewhere:

- Fleet tables, run activity, per-envoy status → `/envoys`, `/runs/{id}`
- Per-framework compliance detail → `/compliance`, `/compliance/{framework}`
- Risk distribution and factor breakdown → `/risk`
- Swarm distribution → `/swarm`, `/swarm/{sha256}`
- Integrations → `/integrations`
- Server.yaml capabilities → `/server/config` (admin-only, sidebar)

## Viz View

Click **Viz View** in the header to switch to `/viz`. This shows a
D3 force-directed fleet bubble chart, convergence donut, and risk histogram.
Auto-refreshes every 60s.

## Data Sources

- Stat cards: FleetCache (precomputed from a single SQL query with 10s TTL,
  shared across all HTMX polling fragments)
- Convergence sparkline: `convergence_history` table (daily snapshots,
  oldest→newest)
- Compliance sparkline: `compliance_history` table, averaged across all
  frameworks per snapshot date
- Risk sparkline: `risk_posture_history` table
- Attention counts: FleetCache counters + RiskData critical count
- Stat cards also enriched with: SecurityScanData, ComplianceIndex, RiskData

## Auto-Refresh

The whole `dashboard-stats` fragment swaps every 30 seconds. Sparklines are
rendered as static SVG, so the swap is flicker-free. No WebSocket or SSE.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
