---
phase: 05-trait-inspection
plan: 01
subsystem: agent
tags: [cobra, cli, json, traits, facts]

# Dependency graph
requires:
  - phase: 02-convergence
    provides: "Fact collectors (OS, hardware, network, packages) with CollectAll and DefaultCollectors"
provides:
  - "isogov-agent traits subcommand for offline trait inspection"
  - "traverseDotPath helper for dot-separated map traversal"
  - "filterCollectors helper for collector name filtering"
  - "formatValue helper for scalar/JSON output formatting"
affects: [05-02, 06-custom-traits]

# Tech tracking
tech-stack:
  added: []
  patterns: ["cobra subcommand with suppressed slog for clean stdout", "dot-path accessor for nested map traversal"]

key-files:
  created:
    - agent/traits.go
    - agent/traits_test.go
  modified: []

key-decisions:
  - "SplitN with recursive descent for dot-path traversal -- handles arbitrary depth and non-identifier keys"
  - "reflect.Kind check for formatValue -- maps/slices get JSON, everything else gets fmt.Sprint"

patterns-established:
  - "Clean CLI output: suppress slog to LevelError for commands that produce structured stdout"
  - "Dot-path accessor pattern: SplitN(path, '.', 2) + recursive descent on map[string]any"

requirements-completed: [INSP-01]

# Metrics
duration: 2min
completed: 2026-03-07
---

# Phase 5 Plan 01: Agent Traits Subcommand Summary

**Cobra subcommand `isogov-agent traits` with collector filtering and dot-path accessor for offline trait inspection**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-07T14:14:42Z
- **Completed:** 2026-03-07T14:16:24Z
- **Tasks:** 1
- **Files modified:** 2

## Accomplishments
- `isogov-agent traits` prints all collected traits as pretty-printed JSON to stdout
- Collector filtering: `isogov-agent traits os hardware` prints only those categories
- Dot-path accessor: `isogov-agent traits os.distro` prints just the scalar value (unquoted)
- slog output suppressed for clean JSON-only stdout, errors to stderr with non-zero exit
- 14 unit tests covering traverseDotPath, filterCollectors, and formatValue

## Task Commits

Each task was committed atomically:

1. **Task 1: Agent traits subcommand (RED)** - `9793dc6` (test)
2. **Task 1: Agent traits subcommand (GREEN)** - `ca93084` (feat)

_TDD task: test commit followed by implementation commit._

## Files Created/Modified
- `agent/traits.go` - Cobra subcommand with traitsCmd, traverseDotPath, filterCollectors, formatValue helpers
- `agent/traits_test.go` - 14 unit tests for helper functions

## Decisions Made
- Used `strings.SplitN(path, ".", 2)` with recursive descent for dot-path traversal -- handles arbitrary depth and non-identifier path segments (e.g., `hardware.disk_gb./`)
- Used `reflect.Kind` to distinguish scalars from maps/slices in formatValue -- scalars get raw `fmt.Sprint`, structured values get `json.MarshalIndent`
- slog suppressed by overriding the default logger to LevelError at start of RunE rather than disabling PersistentPreRunE

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Agent-side traits inspection complete, ready for 05-02 (server REST endpoint + gvctl clients traits)
- traverseDotPath and formatValue patterns can be reused in gvctl for client-side dot-path filtering

---
*Phase: 05-trait-inspection*
*Completed: 2026-03-07*
