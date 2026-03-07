---
phase: 08-resource-conditionals
plan: 02
subsystem: grpc, agent, proto
tags: [conditionals, resource-filtering, when-removal, server-side-eval]

requires:
  - phase: 08-resource-conditionals
    provides: ShouldRunResource function, StringOrSlice type, four conditional fields on ResourceConfig
provides:
  - Server-side resource conditional filtering in configToProtoModules
  - Clean codebase with no when: references (Go, proto, YAML)
  - Simplified agent runner without conditional evaluation logic
affects: [09-conditional-blocks, 10-module-conditionals]

tech-stack:
  added: []
  patterns: [server-side-resource-filtering, proto-reserved-fields]

key-files:
  created: []
  modified:
    - server/grpc/checkin.go
    - server/grpc/checkin_test.go
    - server/config/types.go
    - proto/isogov.proto
    - agent/runner/dag.go
    - agent/runner/runner.go
    - agent/runner/runner_test.go
    - agent/runner/dag_test.go
    - CLAUDE.md

key-decisions:
  - "Server-side filtering: ShouldRunResource called in configToProtoModules before building proto Resource"
  - "Empty modules excluded: if all resources filtered, module not sent to agent"
  - "Proto field 7 reserved: avoids future wire-format conflicts after removing when field"
  - "Agent simplified: no conditional evaluation logic, just executes what server sends"

patterns-established:
  - "Server-side resource filtering: conditions evaluated at check-in, agent receives only runnable resources"
  - "Reserved proto fields: removed fields get reserved to prevent accidental reuse"

requirements-completed: [COND-05, COND-06, COND-07, COND-08, COND-09, COND-10]

duration: 4min
completed: 2026-03-07
---

# Phase 8 Plan 2: Server-Side Resource Filtering and when: Removal Summary

**Server-side resource conditional filtering in configToProtoModules with complete removal of freeform when: field from Go code, proto, and agent runner**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-07T20:39:05Z
- **Completed:** 2026-03-07T20:43:03Z
- **Tasks:** 2
- **Files modified:** 10

## Accomplishments
- configToProtoModules now calls ShouldRunResource to filter resources server-side before building proto responses
- Empty modules (all resources skipped) excluded from proto response entirely
- Complete removal of when: field from ResourceConfig, ResourceNode, proto Module, and all tests
- Agent runner simplified -- no EvaluateWhen, no whenContext, no when.go file
- 4 new checkin tests covering go_if_all_pass, skip_if_any_pass, empty module exclusion, no-conditional passthrough

## Task Commits

Each task was committed atomically:

1. **Wire ShouldRunResource into configToProtoModules** - `730513d` (feat)
2. **Remove when: field from entire codebase** - `0bd9c08` (refactor)

## Files Created/Modified
- `server/grpc/checkin.go` - Added ShouldRunResource call before proto Resource creation, removed when copy
- `server/grpc/checkin_test.go` - 4 new tests for resource conditional filtering
- `server/config/types.go` - Removed When field from ResourceConfig
- `proto/isogov.proto` - Removed when field (reserved 7) from Module message
- `gen/proto/isogov/isogov.pb.go` - Regenerated (no When/GetWhen)
- `gen/proto/isogov/isogov_grpc.pb.go` - Regenerated
- `agent/runner/dag.go` - Removed When from ResourceNode, removed GetWhen call in BuildDAG
- `agent/runner/runner.go` - Removed when evaluation block in executeResource, updated comments
- `agent/runner/when.go` - Deleted (EvaluateWhen no longer needed)
- `agent/runner/when_test.go` - Deleted (11 tests for removed functionality)
- `agent/runner/runner_test.go` - Removed withWhen helper and TestRunnerWhenSkips
- `agent/runner/dag_test.go` - Removed When field from TestBuildDAGModuleLevelMetadata
- `CLAUDE.md` - Updated module system and execution docs to reference conditional fields

## Decisions Made
- Server-side filtering: ShouldRunResource called in configToProtoModules inner loop, before proto Resource creation
- Empty module exclusion: if all resources in a module are filtered, the entire module is excluded from the response
- Proto field 7 reserved to avoid future wire-format conflicts
- Agent fully simplified: no conditional logic remains, just executes what the server sends

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Resource conditional pipeline complete end-to-end: YAML parse -> conditional var evaluation -> ShouldRunResource -> filtered proto response
- Agent receives only runnable resources, no conditional evaluation needed
- Phase 9 (conditional blocks) can build on StringOrSlice and ShouldRunResource
- Phase 10 (module conditionals) can reuse the same evaluation patterns

## Self-Check: PASSED

- SUMMARY.md exists on disk
- Both commits (730513d, 0bd9c08) exist in git history
- when.go and when_test.go confirmed deleted
- No remaining when references in Go code
- All tests pass across entire project
- All three binaries compile

---
*Phase: 08-resource-conditionals*
*Completed: 2026-03-07*
