---
phase: 01-foundation
plan: 05
subsystem: integration
tags: [go, grpc, mtls, tls13, grpc-gateway, rest, cobra, ed25519, signature-verification, testcontainers]

# Dependency graph
requires:
  - phase: 01-01
    provides: Proto stubs, sqlc queries, Makefile, Docker Compose, migrations
  - phase: 01-02
    provides: Secrets Provider, ConfigStore, Classifier, template rendering
  - phase: 01-03
    provides: Enrollment registry (AddApprovedPattern, Register, RevokeClient, ValidateClient)
  - phase: 01-04
    provides: ED25519 keypair generation, SignPayload, VerifyPayload
provides:
  - mTLS gRPC server on :9090 with TLS 1.3 minimum and client cert verification (SEC-01)
  - ED25519 signature verification interceptor with 5-minute replay window (SEC-03)
  - Register RPC handler delegating to enrollment registry
  - CheckIn RPC handler with config resolution, template rendering, revocation check
  - grpc-gateway REST proxy on :8080 for IsgovAdmin service
  - Server main.go wiring all subsystems (secrets, config, DB, migrations, registry)
  - LoadServerConfig with secret: prefix resolution from server.yaml
  - gvctl CLI with cobra subcommand stubs (approved, clients, tokens, push, runs)
  - Seed data command for development (approved patterns, clients, config files)
affects: [02-convergence, 03-operations, 04-web-ui]

# Tech tracking
tech-stack:
  added: [grpc-gateway/runtime, tls13]
  patterns: [mtls-grpc-server, signature-interceptor, grpc-gateway-rest-proxy, cobra-subcommand-stubs, seed-data-idempotent]

key-files:
  created:
    - server/grpc/server.go
    - server/grpc/services.go
    - server/grpc/register.go
    - server/grpc/checkin.go
    - server/grpc/interceptor.go
    - server/grpc/server_test.go
    - server/api/gateway.go
    - server/config/server.go
    - cmd/seed/main.go
  modified:
    - server/main.go
    - cli/main.go
    - .env.example

key-decisions:
  - "Signature payload for CheckIn is the state_fingerprint string; timestamp formatted as Unix epoch string"
  - "Register RPC returns success=false for no-matching-pattern (not gRPC error), PermissionDenied for revoked hostname"
  - "Gateway uses same server cert/key as mTLS client to gRPC backend"
  - "CheckIn renders templates with empty facts map (facts reported separately via ReportFacts)"
  - "Seed command uses ON CONFLICT DO NOTHING for idempotent re-runs"

patterns-established:
  - "mTLS gRPC server: tls.RequireAndVerifyClientCert with MinVersion TLS 1.3"
  - "Signature interceptor skips Register RPC, verifies all other agent RPCs"
  - "grpc-gateway dials gRPC server with same TLS credentials"
  - "Server main.go loads .env -> secrets -> server.yaml -> DB -> migrations -> subsystems -> gRPC -> REST"
  - "Test cert generation with ed25519 CA, server, and client certs in TestMain"

requirements-completed: [SEC-01, SEC-03, SEC-04, SEC-05]

# Metrics
duration: 6min
completed: 2026-03-06
---

# Phase 1 Plan 5: Server Integration Summary

**mTLS gRPC server with signature-verified Register/CheckIn RPCs, grpc-gateway REST proxy, gvctl CLI stubs, and seed data -- wiring all Phase 1 subsystems into a running server**

## Performance

- **Duration:** 6 min
- **Started:** 2026-03-06T01:23:44Z
- **Completed:** 2026-03-06T01:29:52Z
- **Tasks:** 2
- **Files modified:** 12

## Accomplishments
- mTLS gRPC server on :9090 rejects plaintext, requires TLS 1.3 with verified client certificates
- ED25519 signature interceptor validates agent requests with 5-minute replay window, skips Register
- Register handler creates clients via enrollment registry, CheckIn resolves config and renders templates
- Server main.go wires secrets provider, config loader, DB pool, migrations, registry, config store, gRPC, and REST gateway with graceful shutdown
- grpc-gateway REST proxy on :8080 proxies all IsgovAdmin RPCs
- gvctl CLI compiles with cobra subcommands: approved (add/list/revoke), clients (list/show/revoke), tokens (generate), push, runs (list/show)
- 5 integration tests against real PostgreSQL with auto-generated TLS certs

## Task Commits

Each task was committed atomically:

1. **Task 1: mTLS gRPC server, Register/CheckIn handlers, and signature interceptor** - `2ad3ee6` (feat)
2. **Task 2: REST gateway, gvctl CLI stub, and seed data command** - `f823b02` (feat)

## Files Created/Modified
- `server/grpc/server.go` - NewServer with mTLS config, registers IsgovAgent and IsgovAdmin services
- `server/grpc/services.go` - AgentService and AdminService structs with dependencies
- `server/grpc/register.go` - Register RPC handler delegating to registry
- `server/grpc/checkin.go` - CheckIn RPC handler with config resolution and template rendering
- `server/grpc/interceptor.go` - SignatureVerifyInterceptor with timestamp and signature verification
- `server/grpc/server_test.go` - 5 integration tests (mTLS, register, revoked check-in, success check-in, bad signature)
- `server/api/gateway.go` - grpc-gateway REST proxy with mTLS back to gRPC
- `server/config/server.go` - LoadServerConfig with secret: prefix resolution
- `server/main.go` - Full server entrypoint wiring all subsystems
- `cli/main.go` - gvctl CLI with cobra subcommand stubs
- `cmd/seed/main.go` - Seed data command with approved patterns, clients, and sample configs
- `.env.example` - Fixed env var name ISOGOV_TLS_CA to match pathToEnvVar mapping

## Decisions Made
- Signature payload for CheckIn uses the state_fingerprint string with timestamp as Unix epoch string -- consistent with agent/auth signing convention
- Register returns `success=false` (not a gRPC error) for no-matching-pattern, but `PermissionDenied` for revoked hostnames -- allows the agent to distinguish "try a different token" from "permanently blocked"
- Gateway reuses the server's TLS cert/key as its mTLS client identity to the gRPC backend
- CheckIn renders templates with an empty facts map since facts are reported separately via ReportFacts RPC and stored in the DB
- Seed command uses `ON CONFLICT DO NOTHING` for idempotent re-runs without errors
- Fixed .env.example: renamed `ISOGOV_CA_CERT` to `ISOGOV_TLS_CA` to match the `pathToEnvVar("isogov/tls/ca")` mapping used by StaticProvider

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed .env.example env var name mismatch**
- **Found during:** Task 1 (server config loading)
- **Issue:** `.env.example` had `ISOGOV_CA_CERT` but `pathToEnvVar("isogov/tls/ca")` produces `ISOGOV_TLS_CA`
- **Fix:** Renamed `ISOGOV_CA_CERT` to `ISOGOV_TLS_CA` in `.env.example`
- **Files modified:** `.env.example`
- **Verification:** Server config loads correctly with StaticProvider
- **Committed in:** `2ad3ee6` (Task 1 commit)

**2. [Rule 2 - Missing Critical] Added LoadServerConfig function**
- **Found during:** Task 1 (server main.go wiring)
- **Issue:** server/config package had types for ServerConfig but no function to load and resolve secrets from server.yaml
- **Fix:** Created `server/config/server.go` with `LoadServerConfig` that reads YAML and resolves `secret:` prefixed values
- **Files modified:** `server/config/server.go`
- **Verification:** Server compiles and loads config correctly
- **Committed in:** `2ad3ee6` (Task 1 commit)

---

**Total deviations:** 2 auto-fixed (1 bug, 1 missing critical)
**Impact on plan:** Both fixes necessary for the server to start. No scope creep.

## Issues Encountered
None.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Phase 1 Foundation complete: all server-side subsystems wired and tested
- Agent can now implement gRPC client calls to Register and CheckIn (Phase 2)
- gvctl CLI stubs ready for REST API implementation (Phase 3)
- Seed data command available for development workflow
- All 35 server tests pass (8 secrets + 11 config + 19 registry + 5 grpc integration - some shared across plans)

## Self-Check: PASSED

All 11 source files verified present. Both task commits (2ad3ee6, f823b02) confirmed in git log. All 5 gRPC integration tests pass. All 3 binaries compile via `make build`.

---
*Phase: 01-foundation*
*Completed: 2026-03-06*
