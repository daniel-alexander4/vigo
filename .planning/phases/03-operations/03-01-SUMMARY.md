---
phase: 03-operations
plan: 01
subsystem: proto-db-contracts
tags: [proto, grpc, sqlc, migrations, database]
dependency_graph:
  requires: []
  provides: [grpc-phase3-rpcs, db-phase3-queries, force-push-column, compliance-history-table, env-promotions-table]
  affects: [server/grpc, server/api, server/db, cli]
tech_stack:
  added: []
  patterns: [grpc-gateway-annotations, sqlc-query-generation, upsert-on-conflict]
key_files:
  created:
    - migrations/000012_add_force_push_to_clients.up.sql
    - migrations/000013_create_compliance_history.up.sql
    - migrations/000014_create_env_promotions.up.sql
    - server/db/queries/webhooks.sql
    - server/db/queries/package_audit.sql
    - server/db/queries/secret_rotation_events.sql
    - server/db/queries/plans.sql
    - server/db/queries/compliance_history.sql
    - server/db/queries/env_promotions.sql
  modified:
    - proto/isogov.proto
    - server/db/queries/clients.sql
    - server/db/queries/runs.sql
    - gen/proto/isogov/isogov.pb.go
    - gen/proto/isogov/isogov_grpc.pb.go
    - gen/proto/isogov/isogov.pb.gw.go
    - server/db/models.go
decisions:
  - Existing client queries updated to include force_push column for consistency
  - ListAllRuns added to runs.sql rather than a new file for query locality
  - ComplianceSnapshot uses DATE type with UNIQUE constraint for daily aggregation
key_decisions:
  - Updated all existing client SELECT queries to include force_push column to avoid partial struct issues
metrics:
  duration: 2min
  completed: "2026-03-06T03:27:00Z"
---

# Phase 3 Plan 1: Proto + DB Contracts Summary

Proto RPCs and sqlc queries for all Phase 3 features: webhooks, compliance, rollback, package audit, env promote

## What Was Done

### Task 1: Add missing proto RPCs and run code generation
Added 10 new RPCs to IsgovAdmin service with grpc-gateway HTTP annotations:
- **Webhook CRUD:** AddWebhook (POST), ListWebhooks (GET), DeleteWebhook (DELETE)
- **Compliance:** GetComplianceSummary (GET), GetClientCompliance (GET)
- **Rollback:** Rollback (POST) with optional run_id and file_path
- **Package audit:** ListPackages (GET), ListPackageDrift (GET)
- **Env promote:** PromoteEnv (POST)
- **Runs:** ListAllRuns (GET) for cross-client run listing

All corresponding request/response message types added (WebhookInfo, ComplianceSnapshot, PackageInfo, PackageDrift, etc.). Generated Go code compiles cleanly.

### Task 2: New migrations, sqlc queries, and code generation
**Migrations (3 new):**
- 000012: `force_push BOOLEAN` column on clients table
- 000013: `compliance_history` table with daily snapshots and UNIQUE(recorded_at)
- 000014: `env_promotions` table with JSONB diff column

**New sqlc query files (6):**
- `webhooks.sql`: CreateWebhook, ListActiveWebhooks, ListActiveWebhooksByEvent, GetWebhookByID, DeleteWebhook, DeactivateWebhook
- `package_audit.sql`: CreatePackageAudit, ListPackageAuditByClient, ListPackageDrift, ListAllPackageAudit
- `secret_rotation_events.sql`: CreateSecretRotationEvent, ListSecretRotationEvents
- `plans.sql`: CreatePlan, GetPlanByID, ListPlansByClient, CreatePlanResult, ListPlanResults
- `compliance_history.sql`: UpsertComplianceSnapshot, ListComplianceHistory
- `env_promotions.sql`: CreateEnvPromotion, ListEnvPromotions

**Extended existing files:**
- `clients.sql`: SetForcePush, ClearForcePush, ListClientsWithForcePush + updated all existing queries to include force_push column
- `runs.sql`: ListAllRuns

All generated Go code compiles cleanly. Full project `go build ./...` passes.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing critical functionality] Updated existing client queries for force_push column**
- **Found during:** Task 2
- **Issue:** Existing client queries (CreateClient, GetClientByID, GetClientByHostname, ListClients) did not SELECT the new force_push column, which would cause the sqlc-generated Client struct to have a field mismatch
- **Fix:** Updated all existing client SELECT/RETURNING clauses to include force_push
- **Files modified:** server/db/queries/clients.sql
- **Commit:** 92ec0f3

## Verification Results

- `make proto` -- passed
- `make sqlc` -- passed
- `go build ./...` -- passed (full project compiles)
- Proto contains grpc-gateway annotations for all 10 new RPCs
- Migrations sequential: 000012, 000013, 000014

## Commits

| Task | Commit | Description |
|------|--------|-------------|
| 1 | 3d2e873 | feat(03-01): add Phase 3 proto RPCs with grpc-gateway annotations |
| 2 | 92ec0f3 | feat(03-01): add Phase 3 migrations, sqlc queries, and code generation |
