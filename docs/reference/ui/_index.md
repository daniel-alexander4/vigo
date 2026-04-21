
# Web UI

Vigo includes an HTMX-based web dashboard available at `https://server:8443`. The UI provides real-time visibility into fleet health, compliance status, convergence runs, task dispatch, and spanner topology.

The UI is built with Go templates and HTMX for partial page updates without full reloads. Most data tables auto-refresh every 30 seconds.

## Pages

| Page | Path | Description |
|------|------|-------------|
| [Dashboard](dashboard.md) | `/` | Fleet health at a glance |
| [Envoys](envoys.md) | `/envoys` | Enrolled managed nodes |
| [Compliance](compliance.md) | `/compliance` | Fleet compliance overview and trends |
| [Runs](runs.md) | `/runs` | Convergence run history (reached from Envoys → Fleet-wide Runs) |
| [Tasks](task.md) | `/task` | Ad-hoc task dispatch and results |
| [Workflows](workflows.md) | `/workflows` | Multi-step workflow execution |
| [Spanner](spanner.md) | `/spanner` | Hub-spoke topology and health |
| [Swarm](swarm.md) | `/swarm` | P2P blob distribution status and per-envoy progress |
| [Security](security.md) | `/envoys/{id}` | Envoy detail and security info |

### Account

| Page | Path | Description |
|------|------|-------------|
| License | `/license` | License status, tier, node usage, enforcement stage |
| Support | `/support` | Support options and contact info, with your current tier highlighted |

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
| [Server Config](server-config.md) | `/server/config` | Read-only schema view of server.yaml — enabled/disabled per section with all available options |

## Theme

The UI ships with two color themes: **Catppuccin Latte** (light, default) and **Catppuccin Mocha** (dark). A sun/moon toggle in the top navbar flips between them. The choice persists per-browser in `localStorage` and syncs across tabs.

Theme resolution, in order of precedence:

1. Per-operator override from the toggle (stored in `localStorage['vigo_theme']`).
2. Fleet-wide default from `branding.theme` in `server.yaml` (see [server-yaml.md](../server-yaml.md#branding)) — one of `light`, `dark`, or `auto`.
3. The operator's OS preference (`prefers-color-scheme`) when branding is `auto` or unset.

An inline script in the page `<head>` applies the resolved theme class to `<html>` before first paint, so there is no flash of the wrong theme on load or on HTMX boosted navigation.

## Technology

- **Templates**: Go `html/template` with HTMX attributes for dynamic updates
- **Styling**: Static CSS at `ui/static/css/style.css`, Catppuccin palettes via CSS custom properties
- **Auto-refresh**: HTMX `hx-trigger="every 30s"` on data tables and stat cards
- **Data source**: When FleetIndex is available, the UI reads from it for O(1) fleet queries. Falls back to direct database queries otherwise.

The UI is only available in standalone and hub modes. Spoke servers do not serve the web UI.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
