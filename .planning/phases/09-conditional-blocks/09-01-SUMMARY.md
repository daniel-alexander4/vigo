---
phase: 09-conditional-blocks
plan: 01
subsystem: config
tags: [yaml, conditional-block, flattening, go-template]

# Dependency graph
requires:
  - phase: 08-resource-conditionals
    provides: "ResourceConfig with four conditional fields and ShouldRunResource evaluator"
provides:
  - "ConditionalBlock type for grouping resources under shared conditionals"
  - "FlattenResourceNodes function that unwraps conditional_block into plain ResourceConfig list"
  - "Custom UnmarshalYAML on ModuleDefinition for transparent block flattening"
affects: [09-conditional-blocks, 10-module-conditionals]

# Tech tracking
tech-stack:
  added: []
  patterns: [yaml-node-level-parsing, conditional-merge-via-append]

key-files:
  created: [server/config/blocks.go, server/config/blocks_test.go]
  modified: [server/config/types.go]

key-decisions:
  - "Block conditionals merged via append -- block values prepended to resource values"
  - "Custom UnmarshalYAML on ModuleDefinition intercepts resources key, keeping loader.go unchanged"
  - "FlattenResourceNodes operates on raw yaml.Node for conditional_block detection before ResourceConfig decode"

patterns-established:
  - "conditional_block YAML structure: mapping with go_if_*/skip_if_* fields and resources list"
  - "Flattening approach: downstream code (ShouldRunResource, DAG, proto conversion) sees only flat ResourceConfig"

requirements-completed: [COND-11, COND-12, COND-13]

# Metrics
duration: 2min
completed: 2026-03-07
---

# Phase 9 Plan 1: Conditional Block Parsing Summary

**FlattenResourceNodes unwraps conditional_block entries into flat ResourceConfig items with block-level conditionals merged via append onto each child resource**

## Performance

- **Duration:** 2 min
- **Started:** 2026-03-07T22:17:03Z
- **Completed:** 2026-03-07T22:19:42Z
- **Tasks:** 1 (TDD: RED + GREEN)
- **Files modified:** 3

## Accomplishments
- ConditionalBlock type added to types.go for parsing block YAML structure
- FlattenResourceNodes correctly unwraps conditional_block entries, merging all four conditional fields onto child resources via append
- Custom UnmarshalYAML on ModuleDefinition transparently flattens blocks during module loading -- no changes needed to loader.go or any downstream code
- 10 comprehensive tests covering all specified behaviors: passthrough, merging, concatenation, ordering, cross-boundary depends_on, empty block error, all four fields, and YAML integration

## Task Commits

Each task was committed atomically:

1. **Task 1 (RED): Failing tests for block parsing** - `7ef643e` (test)
2. **Task 1 (GREEN): FlattenResourceNodes + custom unmarshal** - `9abf4f7` (feat)

_TDD task: test commit followed by implementation commit._

## Files Created/Modified
- `server/config/blocks.go` - FlattenResourceNodes, mergeConditionals, appendSlices functions
- `server/config/blocks_test.go` - 10 test cases covering all specified behaviors
- `server/config/types.go` - ConditionalBlock type and custom UnmarshalYAML on ModuleDefinition

## Decisions Made
- Block conditionals merged via append (block values prepended to resource values) -- correct for go_if_all_pass and skip_if_any_pass, acceptably approximate for go_if_any_pass and skip_if_all_pass
- Custom UnmarshalYAML on ModuleDefinition handles resources key specially, other keys decoded normally -- keeps loader.go loadModuleDefs unchanged
- FlattenResourceNodes operates on raw yaml.Node slice to detect conditional_block mapping key before attempting ResourceConfig decode

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Block parsing and flattening complete, ready for plan 09-02 (integration with loader end-to-end tests, if applicable)
- All downstream code (ShouldRunResource, configToProtoModules, DAG builder) works unchanged since they only see flat ResourceConfig items

---
*Phase: 09-conditional-blocks*
*Completed: 2026-03-07*
