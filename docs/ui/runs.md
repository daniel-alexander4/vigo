# Runs

The Runs page displays convergence run history across all thralls. Available at `/runs`.

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
| Hostname | Thrall that executed the run |
| Status | Color-coded badge: success (green), failure (red), partial (yellow) |
| Modules | Total module count |
| Changed | Number of modules that made changes |
| Failed | Number of modules that failed |
| Time | Run start timestamp |

The page header shows "{N} runs across all thralls".

## Key Actions

- **Filter by status**: Click a status button to narrow the list
- **View run detail**: Click a run ID to see per-resource results including action taken, errors, and duration for each resource
- **Identify failing thralls**: Sort by failure count to find problematic nodes

## Data Sources

- Run list: Database `ListRuns` query (runs are always stored in the database, even with FleetIndex)
- Run detail: Database `GetRun` with `run_results` join
- Run count: Database count query
