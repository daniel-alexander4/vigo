---
phase: 01-foundation
plan: 03
subsystem: auth
tags: [go, bcrypt, glob, enrollment, registry, testcontainers, postgresql]

# Dependency graph
requires:
  - phase: 01-foundation
    provides: "sqlc query layer for approved_patterns and clients tables"
provides:
  - Enrollment registry with pattern matching, bcrypt token validation, and client revocation
  - Registry.AddApprovedPattern, Register, RevokeClient, ValidateClient API
  - Sentinel errors for enrollment failure modes (ErrNoMatchingPattern, ErrClientRevoked, ErrHostnameRevoked, ErrClientNotFound)
affects: [01-04, 01-05, 02-convergence, 03-operations]

# Tech tracking
tech-stack:
  added: [golang.org/x/crypto/bcrypt, testcontainers-go, golang-migrate]
  patterns: [testcontainers-postgres-integration-tests, tdd-red-green-refactor, glob-first-bcrypt-comparison]

key-files:
  created:
    - server/registry/registry.go
    - server/registry/registry_test.go
  modified:
    - go.mod
    - go.sum

key-decisions:
  - "Used filepath.Match for glob matching (stdlib, covers hostname and IP patterns per spec)"
  - "Glob-first filtering before bcrypt comparison to avoid timing leaks on non-matching patterns"
  - "One-time token enforcement via ListUnusedApprovedPatterns query filter (no separate used check needed)"
  - "ENRL-07 enforced by checking GetClientByHostname for revoked status before pattern matching"
  - "RevokeClient verifies client existence before update (returns ErrClientNotFound for non-existent)"

patterns-established:
  - "testcontainers-go with postgres:16-alpine for integration tests with real DB"
  - "Table truncation between tests for isolation (TRUNCATE ... CASCADE)"
  - "Migrations auto-discovered by walking up from test dir to find migrations/ folder"
  - "Sentinel error pattern for domain-specific failure modes"

requirements-completed: [ENRL-01, ENRL-02, ENRL-03, ENRL-04, ENRL-05, ENRL-06, ENRL-07]

# Metrics
duration: 4min
completed: 2026-03-06
---

# Phase 1 Plan 3: Enrollment Registry Summary

**Enrollment registry with glob pattern matching, bcrypt one-time token validation, client creation, and revocation blocking re-enrollment**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-06T00:59:58Z
- **Completed:** 2026-03-06T01:04:08Z
- **Tasks:** 1 (TDD: 3 commits)
- **Files modified:** 4

## Accomplishments
- Registry implements all 7 enrollment requirements (ENRL-01 through ENRL-07)
- 19 integration tests against real PostgreSQL via testcontainers-go, all passing
- Glob-first filtering before bcrypt comparison prevents timing side-channel leaks
- One-time token enforcement via used_at tracking with automatic filtering
- Revoked clients blocked from check-in (ValidateClient) and re-enrollment (Register hostname check)

## Task Commits

Each task was committed atomically (TDD):

1. **Task 1 RED: Add failing tests for enrollment registry** - `d1a7924` (test)
2. **Task 1 GREEN: Implement enrollment registry** - `363a518` (feat)
3. **Task 1 REFACTOR: Remove unused sentinel errors** - `f745872` (refactor)

## Files Created/Modified
- `server/registry/registry.go` - Enrollment business logic: AddApprovedPattern, Register, RevokeClient, ValidateClient (183 lines)
- `server/registry/registry_test.go` - 19 integration tests covering all enrollment behaviors (571 lines)
- `go.mod` / `go.sum` - Added bcrypt, testcontainers-go, golang-migrate dependencies

## Decisions Made
- Used `filepath.Match` for glob matching -- stdlib, covers hostname and IP patterns per the handoff spec. No need for external glob library.
- Applied glob-first filtering before bcrypt comparison to avoid bcrypt timing cost on patterns that don't even match the hostname/IP. This addresses the plan's pitfall 4 concern.
- One-time token enforcement is handled implicitly by `ListUnusedApprovedPatterns` which only returns patterns where `used_at IS NULL`. No separate "is token used" check needed.
- ENRL-07 (revoked clients cannot re-enroll) enforced by checking `GetClientByHostname` for existing revoked client before any pattern matching occurs.
- `RevokeClient` validates client existence before updating, returning `ErrClientNotFound` for non-existent clients rather than silently succeeding.

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Registry package ready for gRPC handler integration (Plan 01-04 or 02-convergence)
- ValidateClient available for check-in handler to verify client status
- Sentinel errors available for gRPC status code mapping
- testcontainers pattern established for future integration tests

## Self-Check: PASSED

All files verified present. All commits verified in git log (d1a7924, 363a518, f745872). registry.go 183 lines, registry_test.go 571 lines (both exceed 100-line minimum). 19/19 tests pass.

---
*Phase: 01-foundation*
*Completed: 2026-03-06*
