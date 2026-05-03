
# Envoys

The Envoys page lists all enrolled managed nodes. Available at `/envoys`.

## Layout

### Stat Cards

Three cards across the top:

- **Total Envoys** — fleet-wide count.
- **Reachable** — envoys whose last check-in is within the staleness threshold (default `2.5 × checkin.interval`, override via `checkin.offline_threshold` in `server.yaml`).
- **Modules Failing** — fleet-wide failed/total module sum from every envoy's most-recent run. Card border + value tinted along the same gradient as the per-row Convergence badge so the absolute "is anything actually broken?" reading sits next to the relative ones.

### Filter Bar

Three independent axis groups separated by visible gaps:

**Failure axis** — mutually exclusive; the four buckets sum to the fleet total.

- **All** — every envoy.
- **Converged** — last run had zero failed modules. Includes envoys whose last run made changes; "no failures" is the bar, not "no work done."
- **Degraded** — last run had 1–79% failed modules. Any non-zero failure clears the threshold.
- **No Data** — envoy has never reported a run.

The Failed pill (≥80% failed) is hidden by design; severely-failing envoys are surfaced via the Modules Failing card and the gradient-coloured per-row Convergence badge instead. The bucket still exists internally — it just doesn't have its own filter button.

**Drift axis** — overlapping; `Diverged ⊆ Changed`. Counts here can sum past the fleet total because an envoy in DriftDiverged contributes to both buckets.

- **Changed** — last run made changes (`ChangedCount > 0`).
- **Diverged** — `DivergedThreshold` consecutive runs (default `5`) all had changes. Persistent drift loop.

**Reachability** — orthogonal to both above.

- **Offline** — last check-in older than the resolved staleness threshold. Hover renders the resolved value.
- **Observing** — fleet-wide `checkin.observe_only` is set, or the matching envoys.vgo block has `observe_only: true`. Only present when the count is non-zero.

Each pill carries a colored count badge (green for converged, amber for degraded, blue for changed, red for diverged, gray for offline / no data) for quick scanning.

Hostname search with server-side filtering is available below the pills.

### Toolbar

Sits between the filter pills and the table. Outside the polling region so the operator's typed query and chosen sort survive every refresh tick.

- **Search** — case-insensitive substring match on hostname.
- **Sort** — dropdown with: Worst first (status), Last run (newest), Last seen (newest), Hostname (A→Z), IP address. Direction is set automatically per column (temporal columns default to descending; alphabetic columns to ascending).

### Envoys Table

Auto-refreshes every 30 seconds via HTMX. Pagination is offset-based — a hidden `revealed`-triggered row at the end of the tbody loads the next page of 100 rows when scrolled into view, until either all matching rows are shown or the **1,000-row hard cap** is hit. Past the cap the trigger is replaced by a sentinel row that tells the operator to refine the filter — the DOM never grows unbounded.

| Column | Description |
|--------|-------------|
| Hostname | Clickable link to envoy detail page (`/envoys/{id}`) |
| IP Address | Monospaced IP address |
| Agent | Agent binary version reported on last check-in (or `-` if not yet reported) |
| Last Check-in | Relative time ("2m ago") with absolute timestamp on hover. Plain text — reachability is signalled via the Offline pill, not by colouring this cell. |
| Last Convergence | Relative time with absolute timestamp on hover. Clickable link to the run detail page. |
| Root FS | Available disk percentage on `/` — green >20%, yellow ≤20%, red ≤10% |
| Repos | Package repository health — green when all repos are healthy, red with broken count |
| Convergence | Gradient-coloured badge with text `<changed>c · <failed>f / <total>` (e.g. `3c · 1f / 172`). Color interpolates green→yellow→red over `failed/total` 0%→40%→80% via `uifmt.FailureGradient` — the changed count enters the numerator only, never the colour. Hover reveals `"X changed · Y failed of Z modules"`. Envoys with no module count yet render `0c · 0f / 0` in muted gray. Clickable link to run detail. |

### Envoy Count

Displayed in the page header: "{N} enrolled envoys". When filters or search are active, shows "{matching} match(es) of {total}" — the matching count is fleet-wide across every page, not just the current 100-row slice.

## Key Actions

- **Filter by axis**: Click a pill to filter on that axis. Pills across axes combine with AND — `?conv=converged&drift=diverged` shows only envoys that are clean on the failure axis but in a persistent drift loop.
- **View detail**: Click a hostname to see the envoy detail page with full traits, run history, and config.
- **Force convergence**: Available on the envoy detail page.

### Column Sorting

Column headers for Hostname, IP Address, Last Check-in, Last Convergence, and Convergence are clickable to flip direction. The dropdown above the table is the canonical surface — column-header clicks just write into the same `?sort=&dir=` URL parameters. Default landing sort is **Worst first** (`?sort=status`), which lifts the worst-failure-axis envoys to the top of the list — at scale, the alphabetically-first envoy is rarely the one the operator came to fix. Sort and pagination are both applied server-side against the FleetCache.

### Convergence Thresholds

Two orthogonal axes plus reachability. The same algorithm
(`convergence.ComputeConvergence` for failure, `convergence.ComputeDrift`
for drift) drives both the per-row badges and the filter-button counts,
so they can never disagree.

**Failure axis** (mutually exclusive):

| Status | Trigger |
|--------|---------|
| converged | Latest run had zero failed modules. Whether it made changes is irrelevant — drift is the other axis's job |
| degraded | Latest run had 1–79% failed modules |
| failed | Agent reported `blocked`, or a run has ≥80% failed modules. UI-hidden as a pill but tracked internally |
| no data | Envoy has never reported a recognized run status |

**Drift axis** (orthogonal; subset relationship):

| Bucket | Trigger |
|--------|---------|
| changed | Latest run had `ChangedCount > 0` |
| diverged | `DivergedThreshold` (default 5) consecutive runs with changes — persistent drift |

Reachability (Offline) is orthogonal to both; an offline envoy keeps its last-known failure-axis bucket and its drift state.

## Config Trace Viz

The config trace page (`/envoys/{id}/trace`) shows how an envoy's configuration is resolved through the inheritance chain. Click **Viz View** (`/envoys/{id}/trace/viz`) to see:

- **Inheritance tree** — SVG vertical flow showing each config level (common.vgo → roles → node match → environments.vgo) with modules, roles, and vars contributed at each level
- **Module dependency DAG** — D3 force-directed graph of module dependencies (depends_on, before, notify, subscribes) with typed and colored edges. Only shown when dependencies exist.

## Data Sources

- **Envoy list + row status**: FleetIndex (`PaginatedEnvoys`). FleetIndex keeps per-envoy trailing run history (`RecentRuns`, bounded to 10 entries) and recomputes both axes inline on every `ReportResult` (`convergence.ComputeConvergence` for the failure axis, `convergence.ComputeDrift` for the drift axis) — no divergence between row badges and pill counts.
- **Filter button counts**: FleetIndex's precomputed `ConvergenceCounters` — same source feeding the row statuses above. Failure-axis fields sum to total; drift-axis fields overlap.
- **Trailing run history on restart**: rehydrated from the DB via `ListFleetConvergence` (single query, up to `MaxRecentRuns` runs per envoy).
- **Agent version**: `agent_version` column in the envoys table, updated on check-in.
- **Filter** is applied server-side using independent URL query parameters: `?conv=converged`, `?drift=diverged`, `?reach=offline`.
- **Search** is applied server-side using URL query parameter `?search=hostname`.
- **Sort** is applied server-side using URL query parameters `?sort=column&dir=asc`.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
