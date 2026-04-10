
# Dashboard

The dashboard provides fleet health at a glance. It is the landing page at `/`.

## Sidebar

The sidebar navigation appears on all pages. It shows the Vigo logo (or custom org logo if branding is configured), the org name (if set), and the server version (e.g., "v0.2.19") below the logo. The sidebar can be collapsed via the toggle button; version and org name hide when collapsed and reappear on hover.

## Layout

### Version Info Card

A card at the top of the dashboard showing the current server version, agent version, and available agent platforms (OS/architecture combinations). This is static information that does not auto-refresh.

### Stat Cards

Four columns of stat cards, auto-refreshing every 30 seconds:

| Card | Color | Description |
|------|-------|-------------|
| Total Envoys | Brand | Total number of enrolled managed nodes |
| Active (1h) | Green | Envoys that checked in within the last hour |
| Recent Failures | Red | Count of recent failed convergence runs |
| Drift Rate | Yellow | Percentage of envoys with drift detected |

The columns are: **Fleet Health** (total, active, failures, drift rate), **Security Posture** (critical CVEs, high CVEs, avg hardening, rootkit warnings), **Standards Compliance** (compliant %, converged, relapsed, diverged, errors), and **Risk Posture** (avg risk score, critical/high/medium counts). The Risk Posture column includes a 30-day sparkline showing the risk score trend over time.

### Highest Risk Envoys Table

Shows the top 5 riskiest envoys by risk score. Auto-refreshes every 30 seconds. Each row shows hostname, numeric score (0-100), and risk level badge (low/medium/high/critical). Links to envoy detail page.

### Fleet Status Table

Shows the worst 50 envoys sorted by convergence status (failed first, then diverged, relapsed, no data, converged). When more than 50 envoys exist, a "View all N envoys" link appears at the bottom. Auto-refreshes every 60 seconds via HTMX.

| Column | Description |
|--------|-------------|
| Hostname | Link to envoy detail page |
| IP Address | Management IP |
| Last Check-in | Relative time ("2m ago") with absolute timestamp on hover |
| Status | Convergence status badge |

Failed and diverged rows are highlighted with a subtle colored background tint for quick visual scanning.

### Recent Activity Table

A table of the most recent convergence runs across all envoys. Auto-refreshes every 60 seconds.

| Column | Description |
|--------|-------------|
| Run | Truncated run UUID |
| Host | Envoy hostname |
| Status | Color-coded badge (success, failure, partial) |
| Modules | Total module count in the run |
| Changed | Number of modules that made changes |
| Failed | Number of modules that failed |
| Time | Relative timestamp ("2m ago") with absolute time on hover |

### Swarm Distribution Table

Shows all active swarm files (server blobs and envoy seeds) with distribution progress. Only visible when swarm is enabled. Each row links to the swarm detail page.

| Column | Description |
|--------|-------------|
| Name | Blob label (original filename), links to `/swarm/{sha256}` |
| SHA256 | First 8 characters of the content hash |
| Size | Human-readable file size |
| Source | `server` or `seed` badge |
| Progress | Complete/downloading/pending counts with status pills |

## Key Actions

- Click any hostname in the Fleet Status table to view envoy details
- Click any run row to view full run details at `/runs/{id}`
- Stat cards link to filtered views (e.g., clicking "Recent Failures" filters the runs page)

### Onboarding Card

When no envoys are enrolled (first-time setup), the dashboard shows a welcome card with numbered getting-started steps instead of the empty stat grid. The card links to the documentation and disappears automatically once the first envoy enrolls.

## Data Sources

- Stat cards and fleet table: FleetCache (precomputed from a single SQL query with 10s TTL, shared across all HTMX polling fragments)
- Recent runs table: `ListRunsWithHostname` query (runs joined with envoy hostname)
- Risk table: `risk.ScoreFleet()` via FleetIndex traits
- Stat cards also enriched with: SecurityScanData, ComplianceIndex, RiskData
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
