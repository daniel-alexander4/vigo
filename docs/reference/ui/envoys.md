
# Envoys

The Envoys page lists all enrolled managed nodes. Available at `/envoys`.

## Layout

### Filter Bar

Status filter buttons across the top:

- **All** -- show all envoys
- **Converged** -- only converged envoys
- **Relapsed** -- envoys with 2 consecutive runs with changes
- **Diverged** -- envoys with 3+ consecutive runs with changes (persistent conflict)
- **Degraded** -- envoys where the last run was partial (some modules failed, others succeeded)
- **Failed** -- envoys where the last run failed entirely
- **Offline** -- envoys that have not checked in within the stale threshold
- **No Data** -- envoys with no run data

Each button shows a colored count badge (green for converged, red for failed, amber for relapsed/diverged/degraded, gray for offline/no data) for quick scanning.

Hostname search with server-side filtering is available on the right side of the filter bar.

### Envoys Table

Auto-refreshes every 30 seconds via HTMX. Supports cursor-based pagination with infinite scroll.

| Column | Description |
|--------|-------------|
| Hostname | Clickable link to envoy detail page (`/envoys/{id}`) |
| IP Address | Monospaced IP address |
| Agent | Agent binary version reported on last check-in (or `-` if not yet reported) |
| Last Seen | Relative time ("2m ago") with absolute timestamp on hover |
| Last Run | Relative time with absolute timestamp on hover |
| Root FS | Available disk percentage on `/` — green >20%, yellow <=20%, red <=10% |
| Repos | Package repository health — green when all repos are healthy, red with broken count |
| Reachable | Network reachability status from the server's perspective |
| Convergence | Spectrum-colored ratio pill showing `failed/total` (e.g., `0/59` green, `3/59` yellow-green, `47/59` amber, `55/59` red). Color interpolates via HSL from green (0% failures) to red (100% failures). When module count data is unavailable, falls back to a categorical status badge. Clickable link to run detail. |

### Envoy Count

Displayed in the page header: "{N} enrolled envoys". When filters or search are active, shows "{shown} shown of {total}".

## Key Actions

- **Filter by status**: Click a status button to show only matching envoys
- **View detail**: Click a hostname to see the envoy detail page with full traits, run history, and config
- **Force convergence**: Available on the envoy detail page

### Column Sorting

Column headers for Hostname, IP Address, Last Seen, Last Run, and Convergence are clickable to sort ascending or descending. Sorting is applied server-side. Sort state is preserved in URL parameters (`?sort=last_seen&dir=desc`).

### Convergence Thresholds

Status is derived from the latest run's failure percentage plus a trailing
history window. The same algorithm (`convergence.ComputeConvergence`) drives
both the per-row status badges and the filter-button counts, so they can
never disagree.

| Status | Trigger |
|--------|---------|
| converged | Latest run succeeded with no failures and no drift |
| relapsed | 2 consecutive runs with changes (early drift signal) |
| diverged | 3+ consecutive runs with changes (persistent conflict) |
| degraded | Latest run has 1–79% failed modules |
| failed | Latest run has 80–100% failed modules, or any legacy run marked `failure` with no module counts |
| no data | Envoy has never reported a run |

Offline is an orthogonal reachability dimension — every envoy is in exactly
one convergence bucket regardless of whether it's offline.

## Config Trace Viz

The config trace page (`/envoys/{id}/trace`) shows how an envoy's configuration is resolved through the inheritance chain. Click **Viz View** (`/envoys/{id}/trace/viz`) to see:

- **Inheritance tree** — SVG vertical flow showing each config level (common.vgo → roles → node match → environments.vgo) with modules, roles, and vars contributed at each level
- **Module dependency DAG** — D3 force-directed graph of module dependencies (depends_on, before, notify, subscribes) with typed and colored edges. Only shown when dependencies exist.

## Data Sources

- **Envoy list + row status**: FleetIndex (`PaginatedEnvoys`). FleetIndex keeps per-envoy trailing run history (`RecentRuns`, bounded to 10 entries) and recomputes convergence inline on every `ReportResult` via `convergence.ComputeConvergence` — no divergence between row status and counts.
- **Filter button counts**: FleetIndex's precomputed `ConvergenceCounters` — same source feeding the row statuses above, so the buttons can never disagree with what's rendered below them.
- **Trailing run history on restart**: rehydrated from the DB via `ListFleetConvergence` (single query, up to 3 runs per envoy — the diverged-detection horizon).
- **Agent version**: `agent_version` column in the envoys table, updated on check-in.
- **Filter** is applied server-side using URL query parameter `?status=converged`
- **Search** is applied server-side using URL query parameter `?search=hostname`
- **Sort** is applied server-side using URL query parameters `?sort=column&dir=asc`
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
