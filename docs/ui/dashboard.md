# Dashboard

The dashboard provides fleet health at a glance. It is the landing page at `/`.

## Layout

### Stat Cards

Four top-level stat cards, auto-refreshing every 30 seconds:

| Card | Color | Description |
|------|-------|-------------|
| Total Thralls | Brand | Total number of enrolled managed nodes |
| Active (1h) | Green | Thralls that checked in within the last hour |
| Recent Failures | Red | Count of recent failed convergence runs |
| Drift Rate | Yellow | Percentage of thralls with drift detected |

### Recent Activity Table

A table of the most recent convergence runs across all thralls:

| Column | Description |
|--------|-------------|
| Run | Truncated run UUID |
| Host | Thrall hostname |
| Status | Color-coded badge (success, failure, partial) |
| Modules | Total module count in the run |
| Changed | Number of modules that made changes |
| Failed | Number of modules that failed |
| Time | Relative timestamp |

## Key Actions

- Click any run row to view full run details at `/runs/{id}`
- Stat cards link to filtered views (e.g., clicking "Recent Failures" filters the runs page)

## Data Sources

- Stat cards: FleetIndex (`ComplianceSummary()`, `ActiveCount()`, `RecentRuns()`)
- Recent runs table: FleetIndex ring buffer (`RecentRuns(n)`) or database `ListRuns` query
