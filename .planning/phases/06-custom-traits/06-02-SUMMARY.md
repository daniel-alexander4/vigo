---
phase: 06-custom-traits
plan: 02
subsystem: agent
tags: [custom-traits, grpc, sha256, cache, timeout, json, sync]

# Dependency graph
requires:
  - phase: 06-custom-traits
    plan: 01
    provides: "CustomTraitEntry proto, DownloadCustomTrait RPC, traits scanner"
provides:
  - "agent/traits package: Executor with SyncAndRun for manifest-driven sync, cache, timeout execution"
  - "DownloadCustomTrait client method on mTLS channel"
  - "Loop integration: custom traits synced before fact collection, merged into factsMap"
affects: []

# Tech tracking
tech-stack:
  added: []
  patterns: ["Downloader interface for gRPC abstraction in tests", "process group kill for subprocess timeout", "pre-check-in pattern for manifest before fact collection"]

key-files:
  created:
    - agent/traits/executor.go
    - agent/traits/executor_test.go
  modified:
    - agent/checkin/client.go
    - agent/checkin/loop.go
    - agent/checkin/loop_test.go
    - agent/main.go

key-decisions:
  - "Downloader interface abstracts gRPC for test isolation -- mock downloads without real server"
  - "Process group kill (Setpgid + SIGKILL to -pid) ensures child processes of shell scripts also die on timeout"
  - "Pre-check-in with empty fingerprint retrieves manifest before fact collection so custom traits are available in same cycle"
  - "Custom traits cache dir passed as parameter to Loop -- empty string skips traits entirely (backward compatible)"

patterns-established:
  - "Process group kill: cmd.SysProcAttr with Setpgid + cmd.Cancel killing -pid for subprocess trees"
  - "Pre-check-in pattern: lightweight CheckIn call before main loop work to get manifest"

requirements-completed: [CUST-02, CUST-03, CUST-04]

# Metrics
duration: 5min
completed: 2026-03-07
---

# Phase 6 Plan 2: Agent Custom Trait Sync, Execution, and Loop Integration Summary

**Agent-side custom trait executor with manifest-driven SHA256 sync, 10s timeout per executable via process group kill, JSON stdout parsing under custom.* namespace, and daemon loop integration before fact collection**

## Performance

- **Duration:** 5 min
- **Started:** 2026-03-07T14:59:18Z
- **Completed:** 2026-03-07T15:04:21Z
- **Tasks:** 2 (Task 1 via TDD)
- **Files modified:** 6

## Accomplishments
- Custom trait executor syncs from manifest, downloads only new/changed executables, cleans up stale cache
- Each executable runs with configurable timeout using process group kill for clean subprocess termination
- JSON stdout parsed and merged under custom.<name> in the traits map (nested structures preserved)
- Failed/timed-out/invalid-JSON traits logged as warnings and skipped -- never block convergence
- Daemon loop restructured: pre-check-in for manifest, sync traits, collect facts, merge, report, converge
- 10 tests covering sync, cache, execution, timeout, JSON parsing, cleanup, error handling

## Task Commits

Each task was committed atomically:

1. **Task 1 (RED): Failing tests for custom trait executor** - `069e4fd` (test)
2. **Task 1 (GREEN): Custom trait executor implementation** - `95c89c5` (feat)
3. **Task 2: gRPC client method + loop integration** - `b9706e2` (feat)

## Files Created/Modified
- `agent/traits/executor.go` - Executor with SyncAndRun, Downloader interface, cache management, timeout execution, JSON parsing
- `agent/traits/executor_test.go` - 10 tests: sync new/cached/changed, valid/invalid/nested JSON, non-zero exit, timeout, empty manifest, stale cleanup
- `agent/checkin/client.go` - Added DownloadCustomTrait method on mTLS channel
- `agent/checkin/loop.go` - Loop restructured with pre-check-in for manifest, custom traits sync/run before facts, merging into factsMap
- `agent/checkin/loop_test.go` - Updated mocks for DownloadCustomTrait interface, Loop call with traitsCacheDir
- `agent/main.go` - Passes filepath.Join(dataDir, "traits") as cache dir to Loop

## Decisions Made
- Downloader interface abstracts gRPC for test isolation -- mock downloads without real server
- Process group kill (Setpgid + SIGKILL to -pid) ensures child processes of shell scripts also die on timeout
- Pre-check-in with empty fingerprint retrieves manifest before fact collection so custom traits are available in same cycle
- Custom traits cache dir passed as parameter to Loop -- empty string skips traits entirely (backward compatible)
- One-shot `run` command does not sync custom traits (daemon-only feature per CONTEXT.md)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed process group kill for timeout enforcement**
- **Found during:** Task 1 (GREEN phase)
- **Issue:** exec.CommandContext sends SIGKILL to process but shell script child processes (sleep) survived, causing 15s test runtime instead of 500ms
- **Fix:** Set cmd.SysProcAttr with Setpgid=true, cmd.Cancel kills -pid (process group), cmd.WaitDelay=500ms
- **Files modified:** agent/traits/executor.go
- **Verification:** Timeout test completes in 0.50s instead of 15s
- **Committed in:** 95c89c5 (Task 1 GREEN commit)

---

**Total deviations:** 1 auto-fixed (1 bug)
**Impact on plan:** Essential fix for correct timeout behavior with shell script executables. No scope creep.

## Issues Encountered
None beyond the process group kill fix documented above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Phase 6 (Custom Traits) is now complete
- Server-side: trait directory scanning, manifest in CheckInResponse, DownloadCustomTrait RPC (Plan 01)
- Agent-side: manifest-driven sync, cached execution with timeout, JSON parsing, loop integration (Plan 02)
- Custom traits visible through existing Phase 5 tooling (gvctl clients traits, isogov-agent traits) with no changes needed

## Self-Check: PASSED

All 6 source files verified present. All 3 commit hashes verified in git log.

---
*Phase: 06-custom-traits*
*Completed: 2026-03-07*
