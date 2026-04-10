
# Envoys

The Envoys page lists all enrolled managed nodes. Available at `/envoys`.

## Layout

### Filter Bar

Status filter buttons across the top:

- **All** -- show all envoys
- **Converged** -- only converged envoys
- **Relapsed** -- envoys with 2 consecutive runs with changes
- **Diverged** -- envoys with 3+ consecutive runs with changes (persistent conflict)
- **Failed** -- envoys where the last run had errors
- **Offline** -- envoys that have not checked in within the stale threshold
- **No Data** -- envoys with no run data

Each button shows a colored count badge (green for converged, red for failed, amber for relapsed/diverged, gray for offline/no data) for quick scanning.

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
| Convergence | Color-coded convergence status badge (clickable link to run detail for drift/error states) |

### Envoy Count

Displayed in the page header: "{N} enrolled envoys". When filters or search are active, shows "{shown} shown of {total}".

## Key Actions

- **Filter by status**: Click a status button to show only matching envoys
- **View detail**: Click a hostname to see the envoy detail page with full traits, run history, and config
- **Force convergence**: Available on the envoy detail page

### Column Sorting

Column headers for Hostname, IP Address, Last Seen, Last Run, and Convergence are clickable to sort ascending or descending. Sorting is applied server-side. Sort state is preserved in URL parameters (`?sort=last_seen&dir=desc`).

### Row Emphasis

Failed envoy rows are highlighted with a subtle red background tint. Diverged rows get an amber tint. Offline rows get a light gray tint. This provides immediate visual scanning for problems in large fleets.

## Data Sources

- Envoy list: `ListEnvoysPagedWithRun` query (envoys joined with latest run in a single query, no N+1)
- Filter button counts: FleetCache (precomputed, 10s TTL)
- Agent version: `agent_version` column in the envoys table, updated on check-in
- Filter is applied server-side using URL query parameter `?status=converged`
- Search is applied server-side using URL query parameter `?search=hostname`
- Sort is applied server-side using URL query parameters `?sort=column&dir=asc`
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
