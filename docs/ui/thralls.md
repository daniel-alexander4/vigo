# Thralls

The Thralls page lists all enrolled managed nodes. Available at `/thralls`.

## Layout

### Filter Bar

Status filter buttons across the top:

- **All** -- show all thralls
- **Compliant** -- only compliant thralls
- **Drifted** -- thralls where the last run made changes
- **Failing** -- thralls where the last run failed
- **Stale** -- thralls that have not checked in within the stale threshold
- **Unknown** -- thralls with no run data

### Thralls Table

Auto-refreshes every 30 seconds via HTMX.

| Column | Description |
|--------|-------------|
| Hostname | Clickable link to thrall detail page (`/thralls/{id}`) |
| IP Address | Monospaced IP address |
| Enrolled | Enrollment timestamp |
| Last Seen | Last check-in timestamp |
| Status | Color-coded compliance badge |

### Thrall Count

Displayed in the page header: "{N} enrolled thralls".

## Key Actions

- **Filter by status**: Click a status button to show only matching thralls
- **View detail**: Click a hostname to see the thrall detail page with full traits, run history, and config
- **Force push**: Available on the thrall detail page

## Data Sources

- Thrall list: FleetIndex (`AllThralls()`) or database `ListThralls` query
- Compliance status: FleetIndex pre-computed per-thrall compliance
- Filter is applied server-side using URL query parameter `?status=compliant`
