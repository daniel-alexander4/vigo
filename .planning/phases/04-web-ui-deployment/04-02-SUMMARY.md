---
phase: 04-web-ui-deployment
plan: 02
subsystem: infra
tags: [bootstrap, self-update, cross-compile, sha256, http]

# Dependency graph
requires:
  - phase: 04-01
    provides: "REST API gateway and mux for registering bootstrap routes"
provides:
  - "Bootstrap endpoint: curl | bash zero-touch node enrollment"
  - "Agent binary distribution: /api/v1/agent/download/{os}/{arch}"
  - "Agent version API: /api/v1/agent/latest with SHA256 checksums"
  - "Agent self-update: checksum-verified atomic binary replacement with exec restart"
  - "make dist cross-compilation for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64"
affects: [deployment, agent, server]

# Tech tracking
tech-stack:
  added: []
  patterns: [checksum-cache-with-modtime, atomic-rename-same-filesystem, exec-restart, tdd-with-httptest]

key-files:
  created:
    - server/bootstrap/bootstrap.go
    - server/bootstrap/agent.go
    - server/bootstrap/bootstrap_test.go
    - agent/update/updater.go
    - agent/update/updater_test.go
  modified:
    - agent/main.go
    - agent/checkin/loop.go
    - Makefile
    - .gitignore

key-decisions:
  - "text/template for bootstrap script (not html/template) to avoid HTML escaping in shell"
  - "Single-quoted interpolated values in bootstrap script for shell injection prevention"
  - "Checksum cache keyed by file path with modtime invalidation for efficient repeat requests"
  - "ExecFn injection pattern for testable syscall.Exec without replacing process in tests"
  - "UpdateConfig struct passed to Loop for optional self-update (nil disables)"
  - "Self-update failure logged as warning, never crashes agent (retry next cycle)"

patterns-established:
  - "ExecFn: injected function type for testable process replacement"
  - "checksumCache: RWMutex-protected cache with file modification time invalidation"

requirements-completed: [AGNT-10, INFRA-04, INFRA-05]

# Metrics
duration: 5min
completed: 2026-03-06
---

# Phase 4 Plan 2: Bootstrap & Agent Self-Update Summary

**Bootstrap endpoint with curl|bash enrollment, agent binary distribution with SHA256 checksums, and self-update via atomic binary replacement and exec restart**

## Performance

- **Duration:** 5 min
- **Started:** 2026-03-06T20:12:39Z
- **Completed:** 2026-03-06T20:17:46Z
- **Tasks:** 2
- **Files modified:** 9

## Accomplishments
- GET /bootstrap?token=TOKEN serves zero-touch shell script with OS detection, binary download, CA cert install, registration, and optional systemd service install
- GET /api/v1/agent/latest returns version + SHA256 checksum JSON, GET /api/v1/agent/download/{os}/{arch} serves pre-built binaries with OS/arch validation
- Agent daemon checks for updates after each successful convergence run, downloads + verifies checksum + atomically replaces binary + exec restarts
- make dist cross-compiles agent for 4 platform/arch combinations (linux/darwin x amd64/arm64)
- 15 tests total (8 bootstrap, 7 self-update) all passing

## Task Commits

Each task was committed atomically:

1. **Task 1: Bootstrap and agent distribution endpoints** - `3ae1f2c` (test), `b5a37d3` (feat)
2. **Task 2: Agent self-update mechanism** - `6d3f31c` (test), `87d14a4` (feat)

_TDD tasks: test commit (RED) + feat commit (GREEN) for each task_

## Files Created/Modified
- `server/bootstrap/bootstrap.go` - Bootstrap script handler, agent latest, agent download endpoints
- `server/bootstrap/agent.go` - SHA256 checksum computation and caching, binary path helper
- `server/bootstrap/bootstrap_test.go` - 8 tests for all bootstrap endpoints
- `agent/update/updater.go` - CheckForUpdate + Apply with checksum verification and atomic rename
- `agent/update/updater_test.go` - 7 tests for version check, apply, checksum mismatch, cleanup
- `agent/main.go` - Added version var (ldflags), --api-addr flag, version subcommand, UpdateConfig wiring
- `agent/checkin/loop.go` - UpdateConfig struct, checkSelfUpdate after successful convergence
- `Makefile` - VERSION variable, dist target for cross-compilation, dist/ in clean target
- `.gitignore` - Added dist/ directory

## Decisions Made
- Used text/template (not html/template) for bootstrap shell script to avoid HTML entity escaping
- Single-quoted all interpolated values in bootstrap script to prevent shell injection
- Checksum cache uses file modification time for invalidation, avoiding re-hashing unchanged binaries
- ExecFn injection pattern allows testing Apply() without replacing the test process via syscall.Exec
- UpdateConfig passed as pointer to Loop -- nil disables self-update (one-shot run command skips it)
- Self-update failure is non-fatal: logged as warning, agent continues with old version and retries next cycle

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- Bootstrap handler ready to be wired into server/main.go HTTP mux (alongside gateway routes)
- Agent self-update functional for any REST API base URL passed via --api-addr
- make dist produces binaries that can be placed in agentDir for the bootstrap handler to serve

---
*Phase: 04-web-ui-deployment*
*Completed: 2026-03-06*
