# Envoys

The Envoys page lists all enrolled managed nodes. Available at `/envoys`.

## Layout

### Filter Bar

Status filter buttons across the top:

- **All** -- show all envoys
- **Compliant** -- only compliant envoys
- **Drifted** -- envoys where the last run made changes
- **Failing** -- envoys where the last run failed
- **Stale** -- envoys that have not checked in within the stale threshold
- **Unknown** -- envoys with no run data

### Envoys Table

Auto-refreshes every 30 seconds via HTMX.

| Column | Description |
|--------|-------------|
| Hostname | Clickable link to envoy detail page (`/envoys/{id}`) |
| IP Address | Monospaced IP address |
| Enrolled | Enrollment timestamp |
| Last Seen | Last check-in timestamp |
| Status | Color-coded compliance badge |

### Envoy Count

Displayed in the page header: "{N} enrolled envoys".

## Key Actions

- **Filter by status**: Click a status button to show only matching envoys
- **View detail**: Click a hostname to see the envoy detail page with full traits, run history, and config
- **Force push**: Available on the envoy detail page

## Data Sources

- Envoy list: FleetIndex (`AllEnvoys()`) or database `ListEnvoys` query
- Compliance status: FleetIndex pre-computed per-envoy compliance
- Filter is applied server-side using URL query parameter `?status=compliant`
