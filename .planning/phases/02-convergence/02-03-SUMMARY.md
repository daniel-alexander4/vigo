---
phase: 02-convergence
plan: 03
subsystem: agent, api
tags: [gopsutil, facts, grpc, sqlc, jsonb, template-rendering]

# Dependency graph
requires:
  - phase: 01-foundation
    provides: "DB schema (client_facts, file_snapshots, run_results tables), gRPC proto defs, sqlc query pattern, config/template rendering"
provides:
  - "Pluggable Collector interface with 4 fact collectors (OS, hardware, network, packages)"
  - "CollectAll aggregator for nested facts map usable as .Facts.os.family in templates"
  - "ReportResult gRPC handler storing per-module results and finishing runs"
  - "ReportFacts gRPC handler storing JSONB facts with 10-snapshot retention pruning"
  - "sqlc queries for client_facts and file_snapshots tables"
  - "CheckIn reads latest facts from DB for template rendering"
affects: [02-convergence, 03-operations]

# Tech tracking
tech-stack:
  added: [gopsutil/v4 (direct dep)]
  patterns: [pluggable Collector interface, CollectAll error-tolerant aggregation, JSONB fact storage with retention pruning]

key-files:
  created:
    - agent/facts/collector.go
    - agent/facts/os.go
    - agent/facts/hardware.go
    - agent/facts/network.go
    - agent/facts/packages.go
    - agent/facts/collector_test.go
    - server/db/queries/facts.sql
    - server/db/queries/snapshots.sql
    - server/db/facts.sql.go
    - server/db/snapshots.sql.go
    - server/grpc/report.go
    - server/grpc/facts.go
  modified:
    - server/grpc/checkin.go
    - go.mod
    - go.sum

key-decisions:
  - "gopsutil promoted from indirect to direct dependency for cross-platform fact collection"
  - "PackageCollector returns empty map on unsupported platforms (non-fatal, informational)"
  - "PruneClientFacts uses table-aliased subquery to avoid sqlc ambiguous column error"
  - "CheckIn falls through to empty facts map when no facts exist yet (first check-in)"

patterns-established:
  - "Collector interface: Name() + Collect(ctx) pattern for pluggable fact sources"
  - "Error-tolerant collection: CollectAll logs warnings on individual collector failures but continues"
  - "JSONB retention: Insert + prune pattern for bounded history"

requirements-completed: [AGNT-03, AGNT-04, AGNT-05]

# Metrics
duration: 5min
completed: 2026-03-06
---

# Phase 02 Plan 03: Facts & Reporting Summary

**Pluggable fact collectors (OS/hardware/network/packages) via gopsutil with server-side ReportResult/ReportFacts handlers and CheckIn facts integration for template rendering**

## Performance

- **Duration:** 5 min
- **Started:** 2026-03-06T02:05:50Z
- **Completed:** 2026-03-06T02:10:57Z
- **Tasks:** 2
- **Files modified:** 15

## Accomplishments
- Four fact collectors behind Collector interface: OS (family/distro/version/kernel/arch), hardware (cpu_count/memory_mb/disk_gb), network (hostname/fqdn/ip_addresses/mac_addresses), packages (installed map)
- CollectAll aggregates into nested map keyed by collector name, handles individual failures gracefully
- ReportResult handler stores per-module execution results and computes run status (success/failure)
- ReportFacts handler stores JSONB facts with 10-snapshot retention pruning
- CheckIn now reads latest facts from DB for template rendering (enables .Facts.os.family in templates)
- sqlc queries for client_facts (insert/get-latest/prune) and file_snapshots (insert/list)

## Task Commits

Each task was committed atomically:

1. **Task 1: Fact collectors (TDD RED)** - `167ea94` (test)
2. **Task 1: Fact collectors (TDD GREEN)** - `12956bc` (feat)
3. **Task 2: sqlc queries, ReportResult/ReportFacts, CheckIn integration** - `fa822b3` (feat)

## Files Created/Modified
- `agent/facts/collector.go` - Collector interface and CollectAll aggregator
- `agent/facts/os.go` - OS facts via gopsutil host.Info + runtime.GOARCH
- `agent/facts/hardware.go` - Hardware facts via gopsutil cpu/mem/disk
- `agent/facts/network.go` - Network facts via os.Hostname + gopsutil net.Interfaces
- `agent/facts/packages.go` - Package facts via dpkg-query/rpm/brew
- `agent/facts/collector_test.go` - 12 tests covering all collectors and CollectAll
- `server/db/queries/facts.sql` - InsertClientFacts, GetLatestClientFacts, PruneClientFacts
- `server/db/queries/snapshots.sql` - InsertFileSnapshot, ListFileSnapshots
- `server/db/facts.sql.go` - Generated sqlc Go code for facts queries
- `server/db/snapshots.sql.go` - Generated sqlc Go code for snapshot queries
- `server/grpc/report.go` - ReportResult RPC handler
- `server/grpc/facts.go` - ReportFacts RPC handler
- `server/grpc/checkin.go` - Updated to read latest facts from DB

## Decisions Made
- gopsutil promoted from indirect to direct dependency for cross-platform fact collection
- PackageCollector returns empty map on unsupported platforms (non-fatal, informational only)
- PruneClientFacts query uses table-aliased subquery to resolve sqlc ambiguous column reference
- CheckIn falls through to empty facts map when no facts exist yet (expected on first check-in)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] PruneClientFacts query ambiguous column reference**
- **Found during:** Task 2 (sqlc query creation)
- **Issue:** sqlc generate failed with "column reference client_id is ambiguous" in PruneClientFacts subquery
- **Fix:** Added table aliases (cf, sub) to disambiguate column references between outer DELETE and inner SELECT
- **Files modified:** server/db/queries/facts.sql
- **Verification:** sqlc generate succeeded without errors
- **Committed in:** fa822b3 (Task 2 commit)

---

**Total deviations:** 1 auto-fixed (1 blocking)
**Impact on plan:** Minor SQL syntax fix required for sqlc compatibility. No scope creep.

## Issues Encountered
None beyond the sqlc column ambiguity auto-fix above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Fact collectors ready for agent check-in integration (agent sends facts via ReportFacts RPC)
- Template rendering with .Facts namespace fully operational for when expressions and config templates
- ReportResult handler ready for agent run reporting after module execution
- File snapshot queries ready for rollback support implementation

## Self-Check: PASSED

All 12 created files verified present. All 3 commit hashes (167ea94, 12956bc, fa822b3) verified in git log.

---
*Phase: 02-convergence*
*Completed: 2026-03-06*
