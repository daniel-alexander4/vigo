---
phase: 08-resource-conditionals
plan: 01
subsystem: config
tags: [conditionals, resource-evaluation, yaml-types, tdd]

requires:
  - phase: 07-conditional-vars
    provides: ConditionalVar type, ParseConditionalVar, Evaluate, boolean var results in vars map
provides:
  - StringOrSlice custom YAML type for string/list shorthand
  - Four conditional fields on ResourceConfig (GoIfAllPass, GoIfAnyPass, SkipIfAllPass, SkipIfAnyPass)
  - ShouldRunResource evaluation function for server-side resource filtering
affects: [08-resource-conditionals, 09-conditional-blocks, 10-module-conditionals]

tech-stack:
  added: []
  patterns: [gate-then-veto-evaluation, string-or-slice-yaml-unmarshal]

key-files:
  created:
    - server/config/conditionals.go
    - server/config/conditionals_test.go
  modified:
    - server/config/types.go

key-decisions:
  - "Evaluation order: go_if fields gate first, then skip_if fields veto -- ensures gates run before vetoes"
  - "StringOrSlice uses yaml.Node-level unmarshal for clean scalar vs sequence discrimination"
  - "varIsTrue checks string equality to 'true' -- consistent with conditional var boolean convention from phase 7"
  - "Empty conditional lists treated as no constraint (resource runs) -- safe default"
  - "Conditional fields placed before Attributes inline field in struct to prevent YAML inline capture"

patterns-established:
  - "Gate-then-veto evaluation: go_if fields must pass first, then skip_if fields can veto"
  - "StringOrSlice YAML type: reusable for any field accepting string or list shorthand"
  - "Boolean var convention: conditional results stored as 'true'/'false' strings in vars map"

requirements-completed: [COND-05, COND-06, COND-07, COND-08, COND-09]

duration: 2min
completed: 2026-03-07
---

# Phase 8 Plan 1: Resource Conditional Fields and ShouldRunResource Summary

**Four structured conditional fields on ResourceConfig with gate-then-veto evaluation engine replacing freeform when: expressions**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-07T20:34:39Z
- **Completed:** 2026-03-07T20:36:40Z
- **Tasks:** 1 (TDD RED + GREEN)
- **Files modified:** 3

## Accomplishments
- StringOrSlice custom YAML type handles both `go_if_all_pass: is_debian` and `go_if_all_pass: [is_debian, has_mem]`
- Four conditional fields added to ResourceConfig: GoIfAllPass, GoIfAnyPass, SkipIfAllPass, SkipIfAnyPass
- ShouldRunResource evaluates all four fields with gate-then-veto ordering
- 16 new tests covering all individual fields, combinations, edge cases, and YAML unmarshal
- Zero regressions across all 41 config package tests

## Task Commits

Each task was committed atomically:

1. **TDD RED: Failing tests for ShouldRunResource and StringOrSlice** - `c88386f` (test)
2. **TDD GREEN: Implement ShouldRunResource with four conditional fields** - `2379bd8` (feat)

_No refactor commit needed -- implementation was clean and minimal._

## Files Created/Modified
- `server/config/types.go` - Added StringOrSlice type with UnmarshalYAML, four conditional fields on ResourceConfig
- `server/config/conditionals.go` - ShouldRunResource function with varIsTrue helper
- `server/config/conditionals_test.go` - 16 tests for all four fields, combinations, edge cases, YAML unmarshal

## Decisions Made
- Evaluation order: go_if fields gate first, then skip_if fields veto (GoIfAllPass, GoIfAnyPass, SkipIfAllPass, SkipIfAnyPass)
- StringOrSlice uses yaml.Node-level unmarshal for clean scalar vs sequence discrimination
- varIsTrue checks string equality to "true" -- consistent with phase 7 boolean var convention
- Empty conditional lists treated as no constraint (resource runs)
- Conditional fields placed before Attributes inline field in struct to prevent YAML inline capture

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- ShouldRunResource ready for integration into CheckIn pipeline (plan 08-02)
- StringOrSlice type available for reuse in conditional_block and module conditional fields
- All four conditional fields parse correctly from YAML via struct tags
- Gate-then-veto evaluation pattern established for consistent behavior

## Self-Check: PASSED

- All 3 files exist on disk
- All 2 commits (c88386f, 2379bd8) exist in git history
- All tests pass across config package (41 total)
- Full project build succeeds

---
*Phase: 08-resource-conditionals*
*Completed: 2026-03-07*
