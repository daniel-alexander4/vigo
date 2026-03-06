---
phase: 03-operations
plan: 05
subsystem: secrets
tags: [isopass, http-client, tls, secret-rotation, watcher, polling]

# Dependency graph
requires:
  - phase: 01-foundation
    provides: "secrets.Provider interface, StaticProvider, config loader with secret: prefix resolution"
  - phase: 03-02
    provides: "sqlc queries for SetForcePush, CreateSecretRotationEvent, GetClientByHostname"
  - phase: 03-04
    provides: "webhook.Dispatcher for secret.rotated event dispatch"
provides:
  - "IsopassProvider implementing secrets.Provider via isopass HTTP API"
  - "Secret rotation watcher with polling loop and force-push triggering"
  - "TLS CertCache with GetCertificate callback for hot reload"
  - "ConfigStore.ClientsReferencingSecret for mapping secrets to affected clients"
affects: [04-web-ui]

# Tech tracking
tech-stack:
  added: []
  patterns: [interface-based watcher dependencies, atomic cert cache, bearer token exchange]

key-files:
  created:
    - server/secrets/isopass.go
    - server/secrets/isopass_test.go
    - server/watcher/watcher.go
    - server/watcher/watcher_test.go
  modified:
    - server/grpc/server.go
    - server/grpc/server_test.go
    - server/config/loader.go
    - server/config/types.go
    - server/main.go
    - config/server.yaml

key-decisions:
  - "Interface-based watcher deps (VersionProvider, ClientStore, DB, EventDispatcher) for clean unit testing"
  - "CertCache uses sync.RWMutex for concurrent read access during TLS handshakes"
  - "Watcher only starts when IsopassProvider is active (not for dev StaticProvider)"

patterns-established:
  - "VersionProvider interface: GetVersion(ctx, key) (int, error) for secret version polling"
  - "CertCache pattern: atomic cert swap via GetCertificate callback"

requirements-completed: [SECR-08, SECR-09, SECR-10]

# Metrics
duration: 4min
completed: 2026-03-06
---

# Phase 3 Plan 5: Isopass Integration & Secret Rotation Summary

**IsopassProvider with HTTP bearer token auth, secret rotation watcher with force-push triggering, and TLS hot reload via GetCertificate callback**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-06T03:38:17Z
- **Completed:** 2026-03-06T03:42:16Z
- **Tasks:** 2
- **Files modified:** 10

## Accomplishments
- IsopassProvider retrieves secrets via isopass HTTP API with automatic bearer token exchange/renewal
- Secret rotation watcher polls at configurable intervals, detects version bumps, and sets force_push on affected clients
- TLS certificates reload without server restart via CertCache.GetCertificate callback
- Rotation events recorded in secret_rotation_events table and dispatched as secret.rotated webhooks

## Task Commits

Each task was committed atomically:

1. **Task 1: IsopassProvider implementing secrets.Provider via HTTP** - `39e3832` (feat)
2. **Task 2: Secret rotation watcher + TLS hot reload + server wiring** - `c2e557e` (feat)

_Note: TDD tasks with RED/GREEN phases committed as single feat commits after GREEN passed._

## Files Created/Modified
- `server/secrets/isopass.go` - IsopassProvider with HTTP client, token lifecycle, search, versions API
- `server/secrets/isopass_test.go` - Comprehensive tests with httptest mock server
- `server/watcher/watcher.go` - Polling loop with rotation detection, force-push, audit, webhook dispatch
- `server/watcher/watcher_test.go` - Tests for rotation triggering, no-action, multiple clients, run loop
- `server/grpc/server.go` - CertCache type with GetCertificate/Reload, updated buildTLSConfig
- `server/grpc/server_test.go` - Updated for 3-value NewServer return
- `server/config/loader.go` - ClientsReferencingSecret method scanning configs for secret: refs
- `server/config/types.go` - WatcherSection added to ServerConfig
- `server/main.go` - Updated for 3-value NewServer return
- `config/server.yaml` - Added watcher section with enabled flag and poll_interval

## Decisions Made
- Interface-based watcher dependencies (VersionProvider, ClientStore, DB, EventDispatcher) for clean unit testing without real DB or isopass
- CertCache uses sync.RWMutex allowing concurrent read during TLS handshakes while writes (reload) are exclusive
- NewServer now returns (*grpc.Server, *CertCache, error) to expose cert cache for watcher integration
- configReferencesSecret scans vars, env_vars, module vars, and resource attributes exhaustively

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Fixed grpc server_test.go for new 3-value return**
- **Found during:** Task 2 (TLS hot reload)
- **Issue:** Changing NewServer to return 3 values broke existing test
- **Fix:** Updated test to use `srv, _, err :=` pattern
- **Files modified:** server/grpc/server_test.go
- **Verification:** go test ./server/grpc/... passes
- **Committed in:** c2e557e (Task 2 commit)

---

**Total deviations:** 1 auto-fixed (1 bug)
**Impact on plan:** Necessary fix for API change. No scope creep.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Phase 3 (Operations) is now complete with all 5 plans executed
- All server-side infrastructure ready for Phase 4 (Web UI & Deployment)
- IsopassProvider ready for production use when isopass service account is configured

---
*Phase: 03-operations*
*Completed: 2026-03-06*
