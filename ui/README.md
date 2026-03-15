# ui

Server-rendered web UI built with HTMX. Provides a dashboard, node management, run history, and compliance views. All templates are embedded in the binary.

## Summary

- Server-rendered HTML with HTMX for dynamic updates
- Templates embedded via Go `//go:embed`
- Auto-refreshing dashboard and node list (every 30s)
- YAML syntax highlighting for config views
- Compliance trend sparklines (30-day SVG)

## Quickstart

The UI is served on the REST API port (default 8181). Start the server and open:

```
http://localhost:8181/
```

No separate build step is needed -- all assets are compiled into the `hobd` binary.

Authentication is required for all UI and API access. See `server/auth/` for the pluggable provider system (basic, OIDC, isowebauth).

## Pages

| Route | Page | Description |
|-------|------|-------------|
| `/` | Dashboard | Fleet health overview: stat cards, recent runs |
| `/nodes` | Nodes | Node list with compliance status filters |
| `/nodes/{id}` | Node Detail | Single node info, compliance history, last run |
| `/runs` | Runs | Run history with status badges |
| `/runs/{id}` | Run Detail | Per-module results with action badges |
| `/compliance` | Compliance | Fleet compliance gauge, 30-day trend, non-compliant nodes |
| `/config/{hostname}` | Config | Rendered YAML config with syntax highlighting |
| `/plans/{id}` | Plans | Dry-run planned actions |

## Architecture

```
ui/
  embed.go          # //go:embed directives for templates and static assets
  handler.go        # HTTP route registration and request handling
  render.go         # Template parsing, custom functions, page rendering
  sparkline.go      # SVG sparkline generation for compliance trends
  static/
    css/            # Stylesheets
    js/             # HTMX library
  templates/
    base.html       # Layout with sidebar navigation
    pages/
      dashboard.html
      nodes.html
      node-detail.html
      runs.html
      run-detail.html
      compliance.html
      config.html
      plans.html
    partials/
      nav.html          # Sidebar navigation
      stat-card.html    # Reusable stat card component
      status-badge.html # Compliance status badge
      pagination.html   # Page navigation controls
```

## Examples

### HTMX Patterns

Dashboard stat cards refresh every 30 seconds:

```html
<div hx-get="/ui/fragments/dashboard-stats" hx-trigger="every 30s">
  ...
</div>
```

Page navigation uses `hx-boost` for AJAX-powered link clicks without full page reloads:

```html
<nav hx-boost="true">
  <a href="/nodes">Nodes</a>
</nav>
```

Fragment endpoints at `/ui/fragments/*` serve partial HTML for dynamic updates.

### Template Functions

The renderer registers these custom functions for use in templates:

- `statusColor(status)` -- maps compliance status to CSS class
- `sparklineSVG(data, width, height)` -- inline SVG sparkline for 30-day trends
- `formatTime(ts)` -- timestamp formatting
- `highlightYAML(content)` -- YAML syntax highlighting via Chroma lexer
- `truncUUID(uuid)` -- display first 8 characters of a UUID

### Security Headers

All responses include:

- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: SAMEORIGIN`
- `Referrer-Policy: strict-origin-when-cross-origin`
