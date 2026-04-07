
# Web UI

Vigo includes an HTMX-based web dashboard available at `https://server:8443`. The UI provides real-time visibility into fleet health, compliance status, convergence runs, task dispatch, and spanner topology.

The UI is built with Go templates and HTMX for partial page updates without full reloads. Most data tables auto-refresh every 30 seconds.

## Pages

| Page | Path | Description |
|------|------|-------------|
| [Dashboard](dashboard.md) | `/` | Fleet health at a glance |
| [Envoys](envoys.md) | `/envoys` | Enrolled managed nodes |
| [Compliance](compliance.md) | `/compliance` | Fleet compliance overview and trends |
| [Runs](runs.md) | `/runs` | Convergence run history |
| [Tasks](task.md) | `/task` | Ad-hoc task dispatch and results |
| [Workflows](workflows.md) | `/workflows` | Multi-step workflow execution |
| [Query](query.md) | `/query` | Live trait queries |
| [Spanner](spanner.md) | `/spanner` | Hub-spoke topology and health |
| [Swarm](swarm.md) | `/swarm` | P2P blob distribution status and per-envoy progress |
| [Security](security.md) | `/envoys/{id}` | Envoy detail and security info |

### Admin Pages

The following pages are only visible to authenticated admin users.

| Page | Path | Description |
|------|------|-------------|
| [Users](users.md) | `/admin/users` | User account management |
| Enrollment | `/admin/enrollment` | Approved enrollment patterns |
| Tokens | `/admin/tokens` | Bootstrap and API token management |
| Webhooks | `/admin/webhooks` | Webhook endpoint configuration |
| Health | `/admin/health` | System health checks (doctor) |
| Backup | `/admin/backup` | Backup status, snapshot creation, download |

## Technology

- **Templates**: Go `html/template` with HTMX attributes for dynamic updates
- **Styling**: Static CSS at `ui/static/css/style.css`
- **Auto-refresh**: HTMX `hx-trigger="every 30s"` on data tables and stat cards
- **Data source**: When FleetIndex is available, the UI reads from it for O(1) fleet queries. Falls back to direct database queries otherwise.

The UI is only available in standalone and hub modes. Spoke servers do not serve the web UI.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
