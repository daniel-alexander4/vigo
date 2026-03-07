---
phase: 04-web-ui-deployment
plan: 01
subsystem: ui
tags: [htmx, go-embed, go-templates, chroma, sparkline, css]

# Dependency graph
requires:
  - phase: 03-operations
    provides: compliance computation, DB queries (ListClients, ListAllRuns, ListComplianceHistory)
provides:
  - go:embed UI package with template renderer and FuncMap
  - Dashboard page with stat cards and auto-refresh
  - Compliance page with gauge, sparkline, breakdown, non-compliant table
  - Gateway wiring separating UI (/) from API (/api/)
  - CSS design system with custom properties
  - Coming-soon placeholder template for future pages
affects: [04-03-remaining-pages, 04-02-bootstrap-selfupdate]

# Tech tracking
tech-stack:
  added: [alecthomas/chroma/v2, htmx-2.0.4]
  patterns: [per-page-template-cloning, DataSource-interface, HTMX-fragment-polling]

key-files:
  created:
    - ui/embed.go
    - ui/render.go
    - ui/sparkline.go
    - ui/handler.go
    - ui/handler_test.go
    - ui/render_test.go
    - ui/templates/base.html
    - ui/templates/partials/nav.html
    - ui/templates/partials/stat-card.html
    - ui/templates/partials/status-badge.html
    - ui/templates/pages/dashboard.html
    - ui/templates/pages/compliance.html
    - ui/templates/pages/coming-soon.html
    - ui/static/css/style.css
    - ui/static/js/htmx.min.js
  modified:
    - server/api/gateway.go
    - server/main.go
    - go.mod
    - go.sum

key-decisions:
  - "Per-page template cloning avoids name collision pitfall -- each page gets its own clone of base+partials"
  - "DataSource interface decouples handlers from DB for testable mock injection"
  - "grpc-gateway moved from / to /api/ to avoid shadowing UI routes"
  - "Coming-soon placeholder template for stub pages to avoid rendering errors with wrong data types"

patterns-established:
  - "Template cloning: NewRenderer clones base+partials for each page, avoiding {{define}} name collisions"
  - "DataSource interface: production dbDataSource and test mockDataSource implement the same contract"
  - "HTMX fragment handlers: RenderFragment method for partial HTML responses (no full-page wrapper)"
  - "CSS custom properties: --color-success, --color-warning, --color-danger, etc. for consistent theming"

requirements-completed: [UI-01, UI-07, UI-08]

# Metrics
duration: 10min
completed: 2026-03-06
---

# Phase 4 Plan 01: Dashboard & Compliance UI Summary

**HTMX + Go templates web UI with embedded assets, dashboard stat cards with 30s auto-refresh, compliance gauge with SVG sparkline, and gateway route separation**

## Performance

- **Duration:** 10 min
- **Started:** 2026-03-06T20:12:45Z
- **Completed:** 2026-03-06T20:23:00Z
- **Tasks:** 2
- **Files modified:** 18

## Accomplishments
- Complete UI package with go:embed, template renderer with FuncMap (statusColor, sparklineSVG, formatTime, highlightYAML, truncUUID, dict, pct)
- Dashboard page with 4 stat cards auto-refreshing every 30s via HTMX, plus recent activity table
- Compliance page with threshold-colored gauge, breakdown by status, SVG sparkline trend, non-compliant clients table
- Clean ops CSS design system (350+ lines) with dark sidebar, light content area, responsive breakpoints
- Gateway wiring: UI on /, API on /api/, metrics on /metrics -- no route shadowing
- 13 unit tests covering renderer, sparkline, template functions, handler responses, and fragment output

## Task Commits

Each task was committed atomically:

1. **Task 1: Create UI package foundation** - `1c35882` (feat) -- embed, renderer, sparkline, CSS, templates, tests
2. **Task 2: Dashboard and compliance handlers with gateway wiring** - `8395ae8` (feat) -- handlers, handler tests, gateway update, server wiring

## Files Created/Modified
- `ui/embed.go` -- go:embed directive for templates/* and static/*
- `ui/render.go` -- Renderer with per-page cloning, FuncMap, RenderFragment
- `ui/sparkline.go` -- SparklineSVG inline SVG generation
- `ui/handler.go` -- Handler with DataSource interface, Dashboard/Compliance/stub handlers, dbDataSource
- `ui/handler_test.go` -- 6 test functions with mockDataSource
- `ui/render_test.go` -- 7 test functions for renderer and template functions
- `ui/templates/base.html` -- HTML5 layout with hx-boost, sidebar, content area
- `ui/templates/partials/nav.html` -- Dark sidebar navigation with active state
- `ui/templates/partials/stat-card.html` -- Reusable stat card with color border
- `ui/templates/partials/status-badge.html` -- Compliance status badge
- `ui/templates/pages/dashboard.html` -- Dashboard with stats, auto-refresh, activity table
- `ui/templates/pages/compliance.html` -- Gauge, breakdown, sparkline, non-compliant table
- `ui/templates/pages/coming-soon.html` -- Placeholder for unimplemented pages
- `ui/static/css/style.css` -- 350+ lines CSS with custom properties, responsive breakpoints
- `ui/static/js/htmx.min.js` -- Vendored HTMX 2.0.4
- `server/api/gateway.go` -- Updated to mount UI routes, moved gwMux to /api/
- `server/main.go` -- Pass queries, baseInterval, version to NewGateway

## Decisions Made
- Per-page template cloning (clone base+partials per page) to avoid Go template name collision pitfall
- DataSource interface for handler testing without real DB -- mockDataSource in tests, dbDataSource in production
- grpc-gateway catch-all moved from `/` to `/api/` -- gvctl uses `/api/v1/*` so this is backward-compatible
- Coming-soon template for stub pages avoids template execution errors when passing PageData to dashboard template
- `printf "%.0f%%" .DriftRate` in template for formatted percentage display

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Added coming-soon template for stub handlers**
- **Found during:** Task 2 (handler implementation)
- **Issue:** Stub handlers (Clients, Runs) used dashboard template but passed PageData without TotalClients field, causing template execution error
- **Fix:** Created coming-soon.html page template, updated all stub handlers to use it
- **Files modified:** ui/templates/pages/coming-soon.html, ui/handler.go
- **Verification:** All handler content-type tests pass
- **Committed in:** 8395ae8 (Task 2 commit)

---

**Total deviations:** 1 auto-fixed (1 bug)
**Impact on plan:** Necessary for correct template rendering. No scope creep.

## Issues Encountered
None beyond the deviation above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- UI rendering pipeline complete: embed -> parse -> render -> serve
- All subsequent pages (Plan 03) use the same pattern: create page template, add handler method, register route
- DataSource interface ready for expansion with new page data methods
- CSS design system has all needed components (cards, badges, tables, gauges, sparklines)

## Self-Check: PASSED

All 17 files verified present. Both commit hashes (1c35882, 8395ae8) confirmed in git log.

---
*Phase: 04-web-ui-deployment*
*Completed: 2026-03-06*
