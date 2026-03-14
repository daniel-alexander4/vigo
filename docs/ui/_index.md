# Web UI

Hobnob includes an HTMX-based web dashboard available at `http://server:8181`. The UI provides real-time visibility into fleet health, compliance status, convergence runs, orchestration tasks, and federation topology.

The UI is built with Go templates and HTMX for partial page updates without full reloads. Most data tables auto-refresh every 30 seconds.

## Pages

| Page | Path | Description |
|------|------|-------------|
| [Dashboard](dashboard.md) | `/` | Fleet health at a glance |
| [Thralls](thralls.md) | `/thralls` | Enrolled managed nodes |
| [Compliance](compliance.md) | `/compliance` | Fleet compliance overview and trends |
| [Runs](runs.md) | `/runs` | Convergence run history |
| [Tasks](tasks.md) | `/tasks` | Ad-hoc task dispatch and results |
| [Workflows](workflows.md) | `/workflows` | Multi-step workflow execution |
| [Query](query.md) | `/query` | Live trait queries |
| [Federation](federation.md) | `/federation` | Hub-spoke topology and health |
| [Security](security.md) | `/thralls/{id}` | Thrall detail and security info |

## Technology

- **Templates**: Go `html/template` with HTMX attributes for dynamic updates
- **Styling**: Static CSS at `ui/static/css/style.css`
- **Auto-refresh**: HTMX `hx-trigger="every 30s"` on data tables and stat cards
- **Data source**: When FleetIndex is available, the UI reads from it for O(1) fleet queries. Falls back to direct database queries otherwise.

The UI is only available in standalone and hub modes. Spoke servers do not serve the web UI.
