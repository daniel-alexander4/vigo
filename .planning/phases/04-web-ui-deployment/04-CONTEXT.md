# Phase 4: Web UI & Deployment - Context

**Gathered:** 2026-03-06
**Status:** Ready for planning

<domain>
## Phase Boundary

Web dashboard SPA served from the server binary via `go:embed`, providing fleet monitoring (dashboard, clients, runs, config, plans, compliance views). Plus node bootstrap via a server-hosted curl one-liner script and agent self-update with checksum verification.

</domain>

<decisions>
## Implementation Decisions

### SPA Framework & Architecture
- HTMX + Go templates — no JS framework, no build step, no node_modules
- hx-boost for navigation — regular links upgraded to AJAX, bookmarkable URLs, browser back works natively
- Auto-poll via hx-trigger="every 30s" for dashboard stats and client table refresh
- Assets live in `ui/` directory at repo root: `ui/templates/` for Go templates, `ui/static/` for CSS/JS/images
- go:embed in a `ui` package, served by the existing `http.ServeMux` in `server/api/gateway.go`

### Dashboard Layout & Data Hierarchy
- Fleet health at a glance: big stat cards at top (total clients, active in last hour, recent failures, drift rate)
- Compliance percentage bar below stat cards
- Recent activity table at bottom
- Client list: sortable table with hostname, IP, enrolled date, last seen, compliance status badge (color-coded). Filter by status. Click row to drill into detail.
- Client detail: last N runs in a table with expandable rows showing per-module results (action taken, status). Drift history timeline. Facts panel on the side.
- Runs view: filterable run log across all clients
- Config viewer: syntax-highlighted read-only display of client YAML with resolved template values
- Plans view: dry-run plans with per-resource actions, color-coded by action type (create/update/delete/skip)

### Visual Style & Theming
- Clean ops style: dark sidebar navigation, light content area (Grafana/Datadog inspired)
- Single hand-written CSS file with CSS custom properties for colors/spacing — no framework, no build step
- Status badges use semantic colors: green (compliant), yellow (drifted/stale), red (failing), gray (unknown)
- Compliance gauge: large percentage number + horizontal colored progress bar (green/yellow/red by threshold)
- 30-day compliance trend: inline SVG sparkline rendered server-side in Go template — no JS chart library

### Bootstrap Script
- `GET /bootstrap?token=<token>` returns a shell script with token and server address baked in
- Script detects OS + arch, downloads correct agent binary from `/api/v1/agent/download/{os}/{arch}`, installs to `/usr/local/bin/isogov-agent`, copies embedded CA cert, generates keypair, registers with server using the baked-in token
- One-shot, minimal output — designed for `curl <url> | bash` experience
- `gvctl tokens generate` prints the full bootstrap curl command

### Agent Self-Update
- Agent checks version with server during regular check-in (version info in CheckIn response or separate endpoint)
- If newer version available: downloads after completing current convergence run, verifies SHA256 checksum, replaces binary, restarts
- Update failure (bad checksum, write error): log error, keep running old version, retry on next check-in cycle
- Never crashes or leaves a broken binary — conservative and safe

### Claude's Discretion
- Go template organization (base layout, partials, page-specific templates)
- HTMX attribute patterns and swap strategies
- SVG sparkline rendering approach
- YAML syntax highlighting implementation (server-side or lightweight JS)
- Bootstrap script error messages and OS detection details
- Agent restart mechanism after self-update (exec syscall vs systemd restart)
- Exact color values and CSS custom property naming

</decisions>

<specifics>
## Specific Ideas

- Dashboard should feel like Grafana/Datadog — professional ops dashboard, not a consumer app
- Compliance bar should be instantly readable: see the number, see the color, done
- Config viewer shows resolved values (after template rendering), not raw templates
- Bootstrap experience: one curl command, zero decisions by the operator on the target node

</specifics>

<code_context>
## Existing Code Insights

### Reusable Assets
- `server/api/gateway.go`: existing `http.ServeMux` with `/metrics` and grpc-gateway routes — UI routes mount here
- All REST API endpoints already exist via grpc-gateway (clients, runs, compliance, webhooks, etc.)
- `server/compliance/compliance.go`: ComputeStatus with all 5 states — UI reads from compliance API
- `server/metrics/metrics.go`: 9 Prometheus metrics already registered

### Established Patterns
- grpc-gateway REST API on port 8080 — UI consumes same endpoints as gvctl
- slog for structured logging — UI handlers should use same pattern
- `envOrDefault()` pattern in main.go for configuration
- `db.New(pool)` + sqlc queries pattern for data access

### Integration Points
- `server/api/gateway.go` `NewGateway()` — add UI template handler and static file serving to the mux
- `server/main.go` — may need to pass template-specific data (e.g., base URL, version) to gateway
- `go.mod` — no new Go dependencies expected (HTMX is a JS file, go:embed is stdlib)
- Agent `CheckIn` response or new endpoint needed for version check (self-update)
- New REST endpoint `GET /api/v1/agent/download/{os}/{arch}` for binary distribution
- New endpoint `GET /bootstrap` for the bootstrap script

</code_context>

<deferred>
## Deferred Ideas

None — discussion stayed within phase scope

</deferred>

---

*Phase: 04-web-ui-deployment*
*Context gathered: 2026-03-06*
