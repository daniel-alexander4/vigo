---
phase: 02-convergence
plan: 04
subsystem: agent
tags: [dag, kahn-algorithm, topological-sort, notify, subscribe, convergence-runner]

# Dependency graph
requires:
  - phase: 02-convergence/01
    provides: "Executor interface, ResourceResult, ActionTaken, EvaluateWhen, CommandRunner"
  - phase: 02-convergence/02
    provides: "Concrete executors (file, package, service, user, exec)"
  - phase: 02-convergence/03
    provides: "Fact collectors for when condition evaluation"
provides:
  - "DAG construction from proto modules (BuildDAG)"
  - "Topological sort via Kahn's algorithm (TopoSort)"
  - "Subscribes-to-notify normalization"
  - "Convergence runner with DAG execution, when conditions, notify pass (Runner.Run)"
affects: [02-convergence/05, 03-operations]

# Tech tracking
tech-stack:
  added: []
  patterns: ["Kahn's algorithm for topological sort", "two-pass execution (main + notify)", "module-level metadata resolution to resource-level"]

key-files:
  created:
    - agent/runner/dag.go
    - agent/runner/dag_test.go
    - agent/runner/runner.go
    - agent/runner/runner_test.go
  modified: []

key-decisions:
  - "Module-level depends_on/notify/subscribes resolved to resource names during BuildDAG"
  - "Notify map kept separate from sorted list for post-execution processing"
  - "Stable topological ordering via sorted zero-degree node insertion"
  - "Notify pass skipped entirely during dry-run"

patterns-established:
  - "Two-pass execution: main DAG pass then notify pass for changed resources"
  - "Module-to-resource resolution: module-level metadata mapped to all resources in that module"

requirements-completed: [CONV-07, CONV-08, CONV-09, CONV-10]

# Metrics
duration: 4min
completed: 2026-03-06
---

# Phase 2 Plan 4: DAG Runner Summary

**Kahn's algorithm topological sort with two-pass convergence runner (main DAG execution + notify/subscribe triggers)**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-06T02:18:13Z
- **Completed:** 2026-03-06T02:21:51Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments
- DAG construction from proto modules with module-to-resource dependency resolution
- Kahn's algorithm topological sort with circular dependency detection naming participants
- Subscribes-to-notify normalization during DAG construction
- Two-pass convergence runner: main DAG execution with when-condition evaluation, followed by notify pass for changed resources
- 30 total tests passing with race detector (9 DAG + 9 Runner + 12 When)

## Task Commits

Each task was committed atomically:

1. **Task 1: DAG construction and topological sort** - `6cbd9cd` (test) -> `db707a7` (feat)
2. **Task 2: Runner with DAG execution and notify/subscribe** - `b811b67` (test) -> `1f3e938` (feat)

_TDD tasks each have two commits (RED test -> GREEN implementation)_

## Files Created/Modified
- `agent/runner/dag.go` - ResourceNode struct, BuildDAG, TopoSort (Kahn's algorithm)
- `agent/runner/dag_test.go` - 9 tests: no deps, linear chain, diamond, circular, unknown dep, subscribes normalization, module metadata, multiple resources, empty modules
- `agent/runner/runner.go` - Runner struct, NewRunner, Run (two-pass execution with when/notify)
- `agent/runner/runner_test.go` - 9 tests: topological order, when skips, notify trigger, error no-notify, dry-run, module filter, unknown type, subscribes end-to-end, continues on error

## Decisions Made
- Module-level depends_on/notify/subscribes are resolved to resource names during BuildDAG (all resources in depended-on module become dependencies)
- Notify map kept separate from sorted list so runner can process notifications after full execution pass
- Stable topological ordering ensured by sorting zero-in-degree nodes by original index
- Notify pass skipped entirely during dry-run (avoids side effects)
- Resources that error do NOT trigger their notify targets (prevents cascading re-execution on failure)

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- DAG runner and all executors ready for check-in loop integration in Plan 02-05
- Runner accepts proto modules directly from CheckIn response
- When conditions evaluate against facts from Plan 02-03 collectors

---
*Phase: 02-convergence*
*Completed: 2026-03-06*
