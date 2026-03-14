# Compliance

The Compliance page provides a fleet-wide compliance overview with trends. Available at `/compliance`.

## Layout

### Compliance Gauge

A large percentage display with a color-coded progress bar:

- Green (> 80%): fleet is healthy
- Yellow (50-80%): fleet has significant drift
- Red (< 50%): fleet is in poor compliance

### Breakdown Cards

Five inline counters showing the distribution:

| Counter | Color | Description |
|---------|-------|-------------|
| Compliant | Green | Thralls with no changes in the last run |
| Drifted | Yellow | Thralls that converged but had changes |
| Failing | Red | Thralls where the last run failed |
| Stale | Yellow | Thralls that have not checked in |
| Unknown | Gray | Thralls with no run history |

### 30-Day Compliance Trend

An SVG sparkline showing daily compliance percentage over the last 30 days. Only displayed when historical data is available in the `compliance_history` table.

### Non-Compliant Thralls Table

A table listing all thralls that are not compliant:

| Column | Description |
|--------|-------------|
| Hostname | Thrall hostname |
| Status | Compliance status badge |
| Last Seen | Last check-in time |
| Last Run | Last run status badge |

## Key Actions

- Identify non-compliant thralls at a glance
- Track compliance trends over time via the sparkline
- Click through to individual thrall detail pages

## Data Sources

- Compliance counters: FleetIndex (`ComplianceSummary()`)
- Sparkline data: `compliance_history` table (daily snapshots)
- Non-compliant thrall list: FleetIndex, filtered to exclude status "compliant"
