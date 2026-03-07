---
phase: quick
plan: 1
subsystem: server/config, server/grpc
tags: [template-rendering, vars, checkin]
dependency_graph:
  requires: [RenderTemplate]
  provides: [RenderVars]
  affects: [CheckIn handler, resource attribute rendering]
tech_stack:
  added: []
  patterns: [single-pass var rendering before resource rendering]
key_files:
  created: []
  modified:
    - server/config/template.go
    - server/grpc/checkin.go
    - server/config/loader_test.go
decisions:
  - Single-pass rendering (vars see each other's pre-rendered values)
  - Non-string var values silently pass through (no panic on int/bool/map)
  - Template errors wrap the var name for clear diagnostics
metrics:
  duration: 1min
  completed: 2026-03-07
---

# Quick Task 1: Render Module Vars Through RenderTemplate Summary

RenderVars function renders Go template syntax in module var values at check-in time, enabling vars like `greeting: "Hello from {{ .Facts.os.distro }}"` to resolve against agent facts before resource attribute rendering.

## What Was Built

### RenderVars Function (`server/config/template.go`)
- Iterates over var map; renders string values through `RenderTemplate`, passes non-string values through unchanged
- Returns new map with rendered values; wraps template errors with var name context
- Single-pass design documented in function comment

### CheckIn Integration (`server/grpc/checkin.go`)
- `config.RenderVars(vars, facts)` call inserted after vars nil-check and before `configToProtoModules`
- Template errors return `codes.Internal` gRPC status with descriptive message
- Rendered vars flow into resource attribute rendering naturally

### Test Coverage (`server/config/loader_test.go`)
- 6 subtests in `TestRenderVars`: facts reference, vars reference, plain passthrough, non-string skip, invalid template error, empty map
- All existing tests continue to pass (no regression)

## Deviations from Plan

None - plan executed exactly as written.

## Commits

| Commit | Type | Description |
|--------|------|-------------|
| 3d008ea | test | Add failing tests for RenderVars (TDD RED) |
| a28686b | feat | Implement RenderVars and wire into CheckIn (TDD GREEN) |

## Verification

```
go test ./server/config/... -v -count=1  # All 22 tests pass including 6 new
go build ./...                            # Full project builds cleanly
```

## Self-Check: PASSED

- All modified files exist on disk
- Both commit hashes verified in git log
