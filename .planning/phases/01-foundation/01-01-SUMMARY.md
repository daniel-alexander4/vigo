---
phase: 01-foundation
plan: 01
subsystem: infra
tags: [go, grpc, protobuf, buf, postgresql, sqlc, docker, makefile, mtls]

# Dependency graph
requires: []
provides:
  - Go module with all core dependencies (grpc, pgx, cobra, fsnotify, yaml, bcrypt)
  - Proto stubs for IsgovAgent and IsgovAdmin gRPC services with grpc-gateway REST
  - 11 PostgreSQL migration pairs covering all tables (approved_patterns through webhooks)
  - sqlc-generated type-safe query layer for approved_patterns, clients, runs
  - Makefile with build, proto, certs, test, lint, seed, migrate targets
  - Docker Compose for postgres:16 + server with health checks
  - Multi-stage Dockerfiles for server and agent
  - Self-signed CA + server TLS cert generation with proper SANs
affects: [01-02, 01-03, 01-04, 01-05, 02-convergence, 03-operations]

# Tech tracking
tech-stack:
  added: [grpc-go, protobuf, grpc-gateway, pgx/v5, golang-migrate, sqlc, fsnotify, cobra, yaml.v3, bcrypt, godotenv, uuid, testcontainers-go, buf]
  patterns: [buf-codegen, sqlc-query-generation, multi-stage-docker-build, openssl-cert-generation]

key-files:
  created:
    - proto/isogov.proto
    - buf.yaml
    - buf.gen.yaml
    - sqlc.yaml
    - migrations/000001_create_approved_patterns.up.sql
    - migrations/000002_create_clients.up.sql
    - migrations/000003_create_runs.up.sql
    - migrations/000004_create_run_results.up.sql
    - migrations/000005_create_client_facts.up.sql
    - migrations/000006_create_plans.up.sql
    - migrations/000007_create_plan_results.up.sql
    - migrations/000008_create_file_snapshots.up.sql
    - migrations/000009_create_secret_rotation_events.up.sql
    - migrations/000010_create_package_audit.up.sql
    - migrations/000011_create_webhooks.up.sql
    - server/db/queries/approved_patterns.sql
    - server/db/queries/clients.sql
    - server/db/queries/runs.sql
    - gen/proto/isogov/isogov.pb.go
    - gen/proto/isogov/isogov_grpc.pb.go
    - gen/proto/isogov/isogov.pb.gw.go
    - Makefile
    - docker-compose.yml
    - Dockerfile.server
    - Dockerfile.agent
    - .env.example
    - .gitignore
  modified:
    - go.mod
    - go.sum

key-decisions:
  - "Disabled buf lint rules PACKAGE_DIRECTORY_MATCH and SERVICE_SUFFIX to keep flat proto/isogov.proto layout per locked single-file decision"
  - "Updated buf.yaml lint category from deprecated DEFAULT to STANDARD"
  - "Proto output goes to gen/proto/isogov/ (not gen/proto/) for clean Go package import paths"
  - "Stub main.go files created for server, agent, cli binaries to enable make build before implementation"

patterns-established:
  - "buf.gen.yaml v2 with remote plugins for protobuf, grpc, gateway, and openapiv2"
  - "sqlc.yaml with pgx/v5, UUID override to google/uuid.UUID, timestamptz to time.Time, jsonb to json.RawMessage"
  - "Sequential migration numbering 000001-000011 with up/down pairs"
  - "Makefile certs target with SAN-aware openssl cert generation (localhost, isogov, server, 127.0.0.1)"

requirements-completed: [DB-01, DB-02, DB-03, DB-04, SEC-04, SEC-05, INFRA-01, INFRA-02, INFRA-03]

# Metrics
duration: 7min
completed: 2026-03-06
---

# Phase 1 Plan 1: Project Skeleton Summary

**Go module with dual gRPC services (proto + buf), 11 PostgreSQL migrations (sqlc query layer), and full infrastructure (Makefile, Docker Compose, TLS certs)**

## Performance

- **Duration:** 7 min
- **Started:** 2026-03-06T00:49:00Z
- **Completed:** 2026-03-06T00:56:12Z
- **Tasks:** 2
- **Files modified:** 52

## Accomplishments
- Proto definitions for IsgovAgent (Register, CheckIn, ReportResult, ReportFacts) and IsgovAdmin (11 RPCs with REST annotations) compile and generate Go stubs + gateway code
- All 11 database tables defined with UUID PKs, proper foreign keys, indexes, and CHECK constraints matching the handoff spec
- sqlc generates type-safe Go query code for approved_patterns, clients, and runs tables
- Makefile targets (build, proto, certs, test, lint, seed, migrate) all function correctly
- Docker Compose validates with postgres:16 health check; multi-stage Dockerfiles for server and agent

## Task Commits

Each task was committed atomically:

1. **Task 1: Initialize Go module, proto definitions, and buf codegen** - `14c9232` (feat)
2. **Task 2: Database migrations, sqlc queries, and infrastructure files** - `bdf0cb2` (feat)

## Files Created/Modified
- `proto/isogov.proto` - gRPC service definitions with IsgovAgent and IsgovAdmin services
- `buf.yaml` - Buf v2 module config with googleapis and grpc-gateway deps
- `buf.gen.yaml` - Code generation config for protobuf, gRPC, gateway, and OpenAPI
- `gen/proto/isogov/*.go` - Generated Go proto stubs, gRPC, and gateway code
- `gen/openapi/isogov.swagger.json` - Generated OpenAPI spec
- `migrations/000001-000011*.sql` - 22 migration files (11 up + 11 down)
- `server/db/queries/*.sql` - sqlc query definitions for approved_patterns, clients, runs
- `server/db/*.go` - sqlc-generated Go query layer (db.go, models.go, plus per-table files)
- `sqlc.yaml` - sqlc config with pgx/v5 and type overrides
- `Makefile` - Build, proto, certs, test, lint, seed, migrate, clean targets
- `docker-compose.yml` - postgres:16 + server services with health checks
- `Dockerfile.server` / `Dockerfile.agent` - Multi-stage Go build, alpine final image
- `.env.example` - Template with ISOGOV_DB_DSN, TLS cert paths
- `.gitignore` - certs/, .env, bin/, vendor/
- `go.mod` / `go.sum` - Go module with all dependencies
- `server/main.go` / `agent/main.go` / `cli/main.go` - Stub binaries for compilation

## Decisions Made
- Disabled `PACKAGE_DIRECTORY_MATCH` and `SERVICE_SUFFIX` buf lint rules to maintain the flat `proto/isogov.proto` single-file layout specified in the locked decisions. Service names remain `IsgovAgent` and `IsgovAdmin` (without "Service" suffix) for clarity.
- Updated buf lint category from deprecated `DEFAULT` to `STANDARD` per buf v1.66 recommendation.
- Routed proto generation output to `gen/proto/isogov/` (not `gen/proto/`) to produce clean Go import paths at `github.com/isogov/isogov/gen/proto/isogov`.
- Created stub `main.go` for all three binaries so `make build` succeeds before actual implementation (Rule 3: auto-fix blocking issue).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Installed missing buf and sqlc CLI tools**
- **Found during:** Task 1 (proto codegen)
- **Issue:** buf and sqlc CLI tools not installed on the system
- **Fix:** Installed via `go install github.com/bufbuild/buf/cmd/buf@latest` and `go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest`
- **Verification:** Both tools run successfully, `buf --version` returns 1.66.0, `sqlc version` returns v1.30.0

**2. [Rule 3 - Blocking] Created stub main.go for three binaries**
- **Found during:** Task 2 (make build)
- **Issue:** `make build` requires compilable server/main.go, agent/main.go, cli/main.go but none existed
- **Fix:** Created minimal stub main.go files that print "not yet implemented" and exit cleanly
- **Files modified:** server/main.go, agent/main.go, cli/main.go
- **Verification:** `make build` produces bin/isogov-server, bin/isogov-agent, bin/gvctl

**3. [Rule 1 - Bug] Fixed buf lint failures for v2 config**
- **Found during:** Task 1 (buf lint)
- **Issue:** buf lint failed on deprecated DEFAULT category and strict PACKAGE_DIRECTORY_MATCH / SERVICE_SUFFIX rules conflicting with locked single-file proto design
- **Fix:** Updated lint category to STANDARD, added exceptions for PACKAGE_DIRECTORY_MATCH and SERVICE_SUFFIX
- **Files modified:** buf.yaml
- **Verification:** `buf lint` passes cleanly

---

**Total deviations:** 3 auto-fixed (1 bug, 2 blocking)
**Impact on plan:** All auto-fixes necessary for tooling and compilation. No scope creep.

## Issues Encountered
None beyond the auto-fixed deviations above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Proto stubs ready for gRPC server implementation (Plan 01-02)
- DB schema ready for enrollment registry logic (Plan 01-03)
- sqlc query layer ready for server DB access (Plan 01-02 through 01-05)
- Makefile and Docker Compose ready for development workflow
- TLS certs ready for mTLS server setup (Plan 01-02)

## Self-Check: PASSED

All files verified present. All commits verified in git log. 22 migration files (11 up + 11 down). Generated code in gen/proto/isogov/ and server/db/. Both task commits (14c9232, bdf0cb2) confirmed.

---
*Phase: 01-foundation*
*Completed: 2026-03-06*
