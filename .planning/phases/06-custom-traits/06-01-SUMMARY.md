---
phase: 06-custom-traits
plan: 01
subsystem: grpc
tags: [protobuf, grpc, sha256, traits, scanner]

# Dependency graph
requires:
  - phase: 05-trait-inspection
    provides: "Trait inspection CLI and REST endpoint for viewing traits"
provides:
  - "CustomTraitEntry proto message and custom_traits field on CheckInResponse"
  - "DownloadCustomTrait gRPC RPC for serving trait executable bytes"
  - "server/traits package with directory scanner, SHA256 checksums, executable filtering"
affects: [06-custom-traits plan 02 (agent sync), 06-custom-traits plan 03 (agent execution)]

# Tech tracking
tech-stack:
  added: []
  patterns: ["traits scanner with name-to-file cache for ReadFile", "scan-on-checkin for rarely-changing directory"]

key-files:
  created:
    - server/traits/scanner.go
    - server/traits/scanner_test.go
    - server/grpc/traits.go
  modified:
    - proto/isogov.proto
    - server/grpc/services.go
    - server/grpc/server.go
    - server/grpc/checkin.go
    - server/main.go

key-decisions:
  - "Scan traits directory on every CheckIn (no fsnotify watcher -- trait files change rarely)"
  - "Trait name derived from filename by stripping last extension only (cpu.check.sh -> cpu.check)"

patterns-established:
  - "Scanner pattern: ScanDir builds name cache, ReadFile uses cached name-to-filename mapping"
  - "Non-fatal trait errors: scan failures logged as warning, check-in continues without manifest"

requirements-completed: [CUST-01, CUST-02]

# Metrics
duration: 3min
completed: 2026-03-07
---

# Phase 6 Plan 1: Custom Trait Manifest & Download RPC Summary

**Custom trait manifest in CheckInResponse with SHA256 checksums and DownloadCustomTrait gRPC RPC for serving trait executable bytes**

## Performance

- **Duration:** 3 min
- **Started:** 2026-03-07T14:53:00Z
- **Completed:** 2026-03-07T14:56:09Z
- **Tasks:** 2
- **Files modified:** 8

## Accomplishments
- Proto updated with CustomTraitEntry message, custom_traits field on CheckInResponse, and DownloadCustomTrait RPC
- Traits scanner package filters executables, computes SHA256 checksums, strips extensions, handles missing dir
- CheckIn handler embeds manifest in every response; DownloadCustomTrait serves file bytes
- 8 tests covering executable filtering, name derivation, missing dir, ReadFile

## Task Commits

Each task was committed atomically:

1. **Task 1 (RED): Proto + failing scanner tests** - `58cfd02` (test)
2. **Task 1 (GREEN): Traits scanner implementation** - `da137c0` (feat)
3. **Task 2: CheckIn manifest injection + DownloadCustomTrait handler** - `32ff6e4` (feat)

## Files Created/Modified
- `proto/isogov.proto` - Added CustomTraitEntry, DownloadCustomTraitRequest/Response messages, DownloadCustomTrait RPC
- `server/traits/scanner.go` - Directory scanner with SHA256 checksums and executable-bit filtering
- `server/traits/scanner_test.go` - 8 tests covering all scanner behaviors
- `server/grpc/traits.go` - DownloadCustomTrait RPC handler
- `server/grpc/checkin.go` - Manifest injection into CheckInResponse
- `server/grpc/services.go` - Added traitsScanner field to AgentService
- `server/grpc/server.go` - Added TraitsDir to ServerConfig, wired scanner
- `server/main.go` - Wired ISOGOV_TRAITS_DIR env var (default: "traits")

## Decisions Made
- Scan traits directory on every CheckIn rather than using fsnotify watcher (trait files change rarely)
- Trait name derived from filename by stripping last extension only (e.g., cpu.check.sh -> cpu.check)
- Non-fatal trait scan errors: log warning, continue check-in without manifest

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Proto stubs regenerated with all custom trait types
- Scanner package ready for agent-side consumption
- DownloadCustomTrait RPC ready for agent sync
- Next plan (06-02) can implement agent-side trait sync and execution

## Self-Check: PASSED

All 8 source files verified present. All 3 commit hashes verified in git log.

---
*Phase: 06-custom-traits*
*Completed: 2026-03-07*
