---
phase: 04-web-ui-deployment
plan: 03
subsystem: ui
tags: [htmx, go-templates, chroma, css, datasource-pattern]

# Dependency graph
requires:
  - phase: 04-01
    provides: "UI package with Renderer, DataSource interface, dashboard/compliance pages, CSS design system"
  - phase: 04-02
    provides: "Bootstrap handler for server wiring"
  - phase: 03-operations
    provides: "DB queries (ListClients, ListAllRuns, GetPlanByID, ListPlanResults, GetLatestClientFacts), compliance computation"
provides:
  - "Clients list page with status filter and HTMX auto-refresh"
  - "Client detail page with runs, expandable results, drift history, facts panel"
  - "Runs list page with cross-client filterable run log"
  - "Run detail page with per-module results and color-coded actions"
  - "Config viewer with syntax-highlighted raw YAML (secrets not resolved)"
  - "Plans viewer with color-coded planned actions"
  - "ConfigStore.Dir() for raw YAML file access"
  - "Full server wiring with configStore passed through to UI handler"
affects: [deployment]

# Tech tracking
tech-stack:
  added: []
  patterns: [DataSource-extension, configStore-Dir-accessor, planActionColor-mapping, N+1-avoidance-client-map]

key-files:
  created: []
  modified:
    - ui/handler.go
    - ui/handler_test.go
    - ui/templates/pages/clients.html
    - ui/templates/pages/client-detail.html
    - ui/templates/pages/runs.html
    - ui/templates/pages/run-detail.html
    - ui/templates/pages/config.html
    - ui/templates/pages/plans.html
    - ui/templates/partials/pagination.html
    - ui/static/css/style.css
    - server/api/gateway.go
    - server/config/loader.go
    - server/main.go

key-decisions:
  - "Extended DataSource interface with 6 new methods rather than adding per-handler DB access"
  - "ConfigStore.Dir() method exposes raw config directory for reading unresolved YAML files"
  - "planActionColor helper maps create->success, update->info, delete->danger, skip/none->muted"
  - "Client hostname lookup map in RunsData avoids N+1 queries for cross-client run listing"
  - "clients-table HTMX fragment reuses same template define block as full page for DRY rendering"

patterns-established:
  - "DataSource method per page: each UI page has a dedicated data method returning typed page data"
  - "Error-returns for detail pages: ClientDetailData/RunDetailData/PlanDetailData return error for 404 handling"
  - "Filter bar pattern: query param ?status=X filters rows, active filter gets btn--primary class"

requirements-completed: [UI-02, UI-03, UI-04, UI-05, UI-06]

# Metrics
duration: 8min
completed: 2026-03-06
---

# Phase 4 Plan 03: Remaining UI Pages & Server Wiring Summary

**All 7 UI pages functional with filterable client/run tables, expandable per-module results, syntax-highlighted YAML config viewer, color-coded plan actions, and full server wiring with configStore**

## Performance

- **Duration:** 8 min
- **Started:** 2026-03-06T20:26:49Z
- **Completed:** 2026-03-06T20:34:48Z
- **Tasks:** 2 (TDD)
- **Files modified:** 13

## Accomplishments
- 6 new page handlers replacing coming-soon stubs: Clients, ClientDetail, Runs, RunDetail, Config, Plans
- Extended DataSource interface with 6 new methods + full production dbDataSource implementation
- 24 total UI tests passing (11 new for the 6 page handlers + status filtering + 404 handling + fragment rendering)
- ConfigStore.Dir() accessor enables raw YAML reading without secret resolution
- Server fully wired: configStore flows from main.go through gateway.go to UI handler
- All 3 binaries (server, agent, gvctl) compile successfully

## Task Commits

Each task was committed atomically (TDD: test + feat):

1. **Task 1+2 RED: Failing tests for all pages** - `ac154ea` (test) -- 11 new tests, DataSource interface extension, stub methods
2. **Task 1+2 GREEN: Full handler implementation** - `abf7183` (feat) -- all handlers, templates, CSS, wiring

_TDD tasks combined because DataSource interface changes needed to be atomic across all pages_

## Files Created/Modified
- `ui/handler.go` -- Extended DataSource (6 new methods), 13 page data types, 6 handler implementations, full dbDataSource
- `ui/handler_test.go` -- 11 new tests with mockDataSource, testPlan/testPlanResult types, defaultTestData helper
- `ui/templates/pages/clients.html` -- Status filter bar, clients table with HTMX auto-refresh, clients-table fragment
- `ui/templates/pages/client-detail.html` -- Two-column layout, runs table, drift history, facts panel sidebar
- `ui/templates/pages/runs.html` -- Status filter bar, cross-client run log table
- `ui/templates/pages/run-detail.html` -- Run summary stats, per-module results with color-coded action badges
- `ui/templates/pages/config.html` -- Breadcrumb, syntax-highlighted YAML, security note about secret: refs
- `ui/templates/pages/plans.html` -- Plan summary stats, color-coded planned actions table
- `ui/templates/partials/pagination.html` -- Reserved partial for future pagination
- `ui/static/css/style.css` -- Filter bar, breadcrumb, client layout, facts panel, badge--info, run-results-row
- `server/api/gateway.go` -- Added configStore parameter to NewGateway, passes to ui.NewHandler
- `server/config/loader.go` -- Added Dir() method to ConfigStore
- `server/main.go` -- Passes configStore to api.NewGateway call

## Decisions Made
- Extended DataSource interface rather than per-handler DB access -- keeps testability via mock injection
- ConfigStore.Dir() exposes raw directory path for reading unresolved YAML (secrets shown as secret: prefix)
- planActionColor maps actions to CSS classes: create=success(green), update=info(blue), delete=danger(red), skip=muted(gray)
- N+1 avoidance: RunsData builds client hostname map once, used for all run rows
- HTMX clients-table fragment shares the same template define block as the full page (DRY)
- Combined Task 1 and Task 2 TDD phases because DataSource interface extension affected all pages atomically

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

Pre-existing test failure in `agent/checkin/loop_test.go` (from Plan 04-02 adding UpdateConfig parameter without updating the test call site). Not caused by this plan's changes. Logged as out-of-scope.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- All 7 UI pages complete: /, /clients, /clients/{id}, /runs, /runs/{id}, /config/{hostname}, /plans/{id}, /compliance
- Server binary fully wired with UI handler and bootstrap handler
- All Phase 4 requirements implemented (UI-01 through UI-08, AGNT-10, INFRA-04, INFRA-05)
- Ready for Phase 4 verification and milestone close-out

## Self-Check: PASSED

All 13 files verified present. Both commit hashes (ac154ea, abf7183) confirmed in git log.

---
*Phase: 04-web-ui-deployment*
*Completed: 2026-03-06*
