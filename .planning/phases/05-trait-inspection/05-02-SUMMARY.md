---
phase: 05-trait-inspection
plan: 02
subsystem: api
tags: [rest, gvctl, cli, traits, json, httptest]

# Dependency graph
requires:
  - phase: 05-trait-inspection
    provides: "agent traits CLI command (05-01)"
provides:
  - "REST endpoint GET /api/v1/clients/{hostname}/traits"
  - "gvctl clients traits subcommand with dot-path accessor"
  - "TraitsHandler pattern for direct REST handlers in gateway"
affects: [06-custom-traits]

# Tech tracking
tech-stack:
  added: []
  patterns: [direct-handler-registration, mock-dbtx-testing, dot-path-traversal]

key-files:
  created:
    - server/api/traits.go
    - server/api/traits_test.go
    - cli/clients_test.go
  modified:
    - server/api/gateway.go
    - cli/clients.go

key-decisions:
  - "Used newTraitsHandlerWithDB helper for test injection since sqlc generates concrete Queries struct"
  - "Duplicated traverseDotPath in cli (15 lines) rather than introducing shared package between binaries"
  - "Direct handler on ServeMux (more specific route wins over /api/ catch-all) instead of grpc-gateway proxy"

patterns-established:
  - "Mock DBTX pattern: implement db.DBTX interface with queryRowFn closure for handler testing"
  - "Direct REST handler pattern: RegisterRoutes on ServeMux before grpc-gateway catch-all"

requirements-completed: [INSP-02, INSP-03]

# Metrics
duration: 3min
completed: 2026-03-07
---

# Phase 5 Plan 2: Server Traits Endpoint & gvctl CLI Summary

**REST endpoint for client traits by hostname plus gvctl clients traits subcommand with dot-path accessor**

## Performance

- **Duration:** 3 min
- **Started:** 2026-03-07T14:15:01Z
- **Completed:** 2026-03-07T14:18:39Z
- **Tasks:** 2
- **Files modified:** 5

## Accomplishments
- GET /api/v1/clients/{hostname}/traits returns JSON envelope with hostname, collected_at (RFC3339), and raw traits
- 404 responses with clear messages for unknown hostname and missing traits
- gvctl clients traits prints full envelope or dot-path extracted scalar value
- 10 unit tests covering handler scenarios and helper functions

## Task Commits

Each task was committed atomically:

1. **Task 1: Server REST endpoint for client traits by hostname**
   - `77989da` (test) - Failing tests for traits handler
   - `04d45e1` (feat) - Implement TraitsHandler, wire into gateway.go
2. **Task 2: gvctl clients traits subcommand with dot-path accessor**
   - `8f6f4b6` (test) - Failing tests for traverseDotPath and formatTraitValue
   - `a694cff` (feat) - Implement clientsTraitsCmd, helpers, register in init()

## Files Created/Modified
- `server/api/traits.go` - TraitsHandler with HandleGetTraits, writeJSONError, newTraitsHandlerWithDB
- `server/api/traits_test.go` - 3 handler tests: success, client not found, no facts
- `server/api/gateway.go` - Wire TraitsHandler.RegisterRoutes before bootstrap
- `cli/clients.go` - clientsTraitsCmd, traverseDotPath, formatTraitValue
- `cli/clients_test.go` - 7 tests for dot-path traversal and value formatting

## Decisions Made
- Used `newTraitsHandlerWithDB(dbtx)` helper to inject mock DBTX into tests, since sqlc generates concrete `*Queries` methods (no interface). This avoids test-only interfaces while keeping tests unit-level.
- Duplicated `traverseDotPath` in cli package (~15 lines) rather than creating a shared internal package. The agent and CLI are separate binaries with no shared internal code, and the function is trivial.
- Registered TraitsHandler directly on ServeMux (Go 1.22+ pattern `GET /api/v1/clients/{hostname}/traits`). More specific routes take priority over the `/api/` catch-all for grpc-gateway.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed mock DBTX interface signature**
- **Found during:** Task 1 (GREEN phase)
- **Issue:** Mock DBTX methods used `interface{}` instead of `context.Context` as first parameter
- **Fix:** Changed mock method signatures to match `db.DBTX` interface exactly
- **Files modified:** server/api/traits_test.go
- **Verification:** Tests compile and pass
- **Committed in:** 04d45e1 (part of Task 1 GREEN commit)

---

**Total deviations:** 1 auto-fixed (1 bug)
**Impact on plan:** Trivial type mismatch in test mock. No scope change.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Traits inspection fully operational: agent can dump traits locally, server exposes them via REST, gvctl provides CLI access
- Ready for Phase 6: Custom Traits (server-side custom traits file pushed to agents)

## Self-Check: PASSED

All 4 files verified present. All 4 commits verified in git log.

---
*Phase: 05-trait-inspection*
*Completed: 2026-03-07*
