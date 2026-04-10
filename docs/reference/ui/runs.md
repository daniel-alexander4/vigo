
# Runs

The Runs page displays convergence run history across all envoys. Available at `/runs`.

## Layout

### Filter Bar

Status filter buttons:

- **All** -- show all runs
- **Success** -- only successful runs
- **Failure** -- only failed runs
- **Partial** -- runs that partially succeeded

### Runs Table

| Column | Description |
|--------|-------------|
| Run | Truncated run UUID, clickable link to run detail (`/runs/{id}`) |
| Hostname | Envoy that executed the run |
| Status | Color-coded badge: success (green), failure (red), partial (yellow) |
| Modules | Total module count |
| Changed | Number of modules that made changes |
| Failed | Number of modules that failed |
| Time | Relative time ("2m ago") with absolute timestamp on hover |

### Column Sorting

Column headers for Hostname, Status, Changed, Failed, and Time are clickable to sort ascending or descending. Sorting is applied server-side via `?sort=column&dir=asc|desc` query parameters.

## Key Actions

- **Filter by status**: Click a status button to narrow the list
- **Sort by column**: Click column headers to sort (e.g., sort by Failed descending to find the worst runs)
- **View run detail**: Click a run ID to see per-resource results including action taken, failed, and duration for each resource

## Data Sources

- Run list: `ListRunsWithHostname` query (runs joined with envoy hostname in a single query)
- Run detail: Database `GetRun` with `run_results` join
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
