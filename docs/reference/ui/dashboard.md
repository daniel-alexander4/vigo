
# Dashboard

The dashboard provides fleet health at a glance. It is the landing page at `/`.

## Layout

### Stat Cards

Four columns of stat cards, auto-refreshing every 30 seconds:

| Card | Color | Description |
|------|-------|-------------|
| Total Envoys | Brand | Total number of enrolled managed nodes |
| Active (1h) | Green | Envoys that checked in within the last hour |
| Recent Failures | Red | Count of recent failed convergence runs |
| Drift Rate | Yellow | Percentage of envoys with drift detected |

The columns are: **Fleet Health** (total, active, failures, drift rate), **Security Posture** (critical CVEs, high CVEs, avg hardening, rootkit warnings), **Standards Compliance** (compliant %, converged, drifted, errors), and **Risk Posture** (avg risk score, critical/high/medium counts).

### Highest Risk Envoys Table

Shows the top 5 riskiest envoys by risk score. Auto-refreshes every 30 seconds. Each row shows hostname, numeric score (0-100), and risk level badge (low/medium/high/critical). Links to envoy detail page.

### Fleet Status Table

Shows every active envoy with its last check-in time. This ensures all envoys are visible on the dashboard, even those with no modules assigned (which would not appear in the Recent Activity table). Auto-refreshes every 30 seconds.

| Column | Description |
|--------|-------------|
| Hostname | Link to envoy detail page |
| IP Address | Management IP |
| Last Check-in | When the envoy last contacted the server |
| Status | Compliance status badge |

### Recent Activity Table

A table of the most recent convergence runs across all envoys. Auto-refreshes every 30 seconds.

| Column | Description |
|--------|-------------|
| Run | Truncated run UUID |
| Host | Envoy hostname |
| Status | Color-coded badge (success, failure, partial) |
| Modules | Total module count in the run |
| Changed | Number of modules that made changes |
| Failed | Number of modules that failed |
| Time | Relative timestamp |

## Key Actions

- Click any hostname in the Fleet Status table to view envoy details
- Click any run row to view full run details at `/runs/{id}`
- Stat cards link to filtered views (e.g., clicking "Recent Failures" filters the runs page)

## Data Sources

- Stat cards: FleetIndex (`ComplianceSummary()`, `ActiveCount()`, `RecentRuns()`), SecurityScanData, RiskData
- Fleet status table: FleetIndex (all non-revoked envoys)
- Recent runs table: FleetIndex ring buffer (`RecentRuns(n)`) or database `ListRuns` query
- Risk table: `risk.ScoreFleet()` via FleetIndex traits
