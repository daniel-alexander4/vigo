---
phase: 10-module-conditionals
plan: 01
subsystem: config
tags: [go, yaml, conditionals, grpc, module-ref]

# Dependency graph
requires:
  - phase: 08-resource-conditionals
    provides: "ShouldRunResource, StringOrSlice, varIsTrue, four conditional fields pattern"
  - phase: 09-conditional-blocks
    provides: "conditional_block flattening, ModuleDefinition custom UnmarshalYAML"
provides:
  - "ModuleRef type with custom YAML unmarshaler (string and object forms)"
  - "ShouldIncludeModule function for module-level conditional evaluation"
  - "Module-level conditional filtering in configToProtoModules check-in pipeline"
affects: []

# Tech tracking
tech-stack:
  added: []
  patterns: ["ModuleRef polymorphic YAML type (scalar or mapping)", "Module-level gate-then-veto conditional evaluation"]

key-files:
  created: []
  modified:
    - server/config/types.go
    - server/config/conditionals.go
    - server/config/loader.go
    - server/config/loader_test.go
    - server/config/conditionals_test.go
    - server/grpc/checkin.go
    - server/grpc/checkin_test.go

key-decisions:
  - "ModuleRef uses alias-based UnmarshalYAML to avoid infinite recursion"
  - "First ModuleRef wins for conditional fields during dedup (matching var merge behavior)"
  - "Module-level ShouldIncludeModule check placed before resource loop in configToProtoModules"

patterns-established:
  - "ModuleRef polymorphic YAML: scalar string or mapping with conditional fields"
  - "Module-level conditionals use same gate-then-veto pattern as resource conditionals"

requirements-completed: [COND-14, COND-15]

# Metrics
duration: 4min
completed: 2026-03-08
---

# Phase 10 Plan 01: Module Conditionals Summary

**ModuleRef type with polymorphic YAML unmarshaler enabling go_if_all_pass/skip_if_any_pass on module references in roles and clients.yaml, evaluated at check-in time via ShouldIncludeModule**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-08T02:02:29Z
- **Completed:** 2026-03-08T02:06:43Z
- **Tasks:** 2
- **Files modified:** 7

## Accomplishments
- ModuleRef type supports both plain string (`- nginx`) and object form (`- {name: nginx, go_if_all_pass: is_debian}`) in YAML
- ShouldIncludeModule uses identical gate-then-veto pattern as ShouldRunResource for all four conditional fields
- Module-level conditional filtering wired into configToProtoModules -- entire modules skipped when conditions fail
- Full backward compatibility with existing plain-string module references in roles and clients.yaml
- 22 new tests (14 config + 8 grpc), all existing tests pass with zero regressions

## Task Commits

Each task was committed atomically:

1. **Task 1: ModuleRef type, ShouldIncludeModule, and loader updates**
   - `0dfc6ec` (test: failing tests for ModuleRef and ShouldIncludeModule)
   - `83f332f` (feat: ModuleRef type, ShouldIncludeModule, loader updates)
2. **Task 2: Wire module conditional filtering into check-in pipeline**
   - `c624429` (test: failing tests for module-level conditional filtering)
   - `d4fce2b` (feat: wire ShouldIncludeModule into configToProtoModules)

_TDD: each task has RED (test) then GREEN (feat) commits_

## Files Created/Modified
- `server/config/types.go` - Added ModuleRef type with custom UnmarshalYAML, conditional fields on ModuleConfig, updated RoleDefinition and ClientEntry to use []ModuleRef
- `server/config/conditionals.go` - Added ShouldIncludeModule with same gate-then-veto pattern as ShouldRunResource
- `server/config/loader.go` - Updated resolveEntry to handle ModuleRef, carry conditional fields into ModuleConfig
- `server/config/conditionals_test.go` - ModuleRef YAML unmarshal tests, ShouldIncludeModule evaluation tests
- `server/config/loader_test.go` - Role/client conditional module ref integration tests, backward compatibility tests
- `server/grpc/checkin.go` - Added ShouldIncludeModule call before resource loop in configToProtoModules
- `server/grpc/checkin_test.go` - Six module-level conditional filtering tests

## Decisions Made
- ModuleRef uses type alias in UnmarshalYAML to avoid infinite recursion (standard Go yaml pattern)
- First ModuleRef wins for conditional fields during dedup, matching existing var merge behavior
- Module-level ShouldIncludeModule placed before resource loop -- avoids unnecessary resource processing for skipped modules

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- v1.2 Structured Conditionals milestone complete (phases 7-10)
- All conditional features implemented: conditional vars, resource conditionals, conditional blocks, module conditionals
- Full test coverage across all conditional layers

## Self-Check: PASSED

All 7 modified files verified present. All 4 task commits verified in git log.

---
*Phase: 10-module-conditionals*
*Completed: 2026-03-08*
