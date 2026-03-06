---
phase: 01-foundation
plan: 02
subsystem: secrets, config
tags: [go, secrets, yaml, fsnotify, classification, templates, godotenv]

# Dependency graph
requires:
  - phase: 01-01
    provides: Go module with core dependencies, project structure
provides:
  - Provider interface for pluggable secret backends (Get/GetBytes/Close)
  - StaticProvider reading .env files via godotenv
  - SecretString type that redacts on String/JSON/slog (never leaks secrets)
  - ConfigStore loading per-client YAML with secret resolution and hot-reload
  - Classifier auto-assigning modules by hostname glob from classification.yaml
  - Template rendering with .Facts and .Vars namespaces
  - Example config files (server.yaml, clients/example.yaml, classification.yaml)
affects: [01-03, 01-04, 01-05, 02-convergence, 03-operations]

# Tech tracking
tech-stack:
  added: [godotenv, fsnotify, yaml.v3]
  patterns: [secret-prefix-resolution, known-secret-enforcement, validate-then-swap, first-match-classification]

key-files:
  created:
    - server/secrets/provider.go
    - server/secrets/secret_string.go
    - server/secrets/static.go
    - server/secrets/static_test.go
    - server/config/types.go
    - server/config/loader.go
    - server/config/classifier.go
    - server/config/template.go
    - server/config/loader_test.go
    - config/server.yaml
    - config/clients/example.yaml
    - config/classification.yaml
  modified:
    - go.mod
    - go.sum

key-decisions:
  - "Key path to env var mapping: isogov/db/dsn -> ISOGOV_DB_DSN (uppercase, slashes to underscores)"
  - "Known-secret fields (dsn, tls_cert_path, tls_key_path, webhook_secret) reject plaintext at load time"
  - "Unconfigured clients receive empty module list (non-nil ClientConfig, empty Modules slice)"
  - "Classification uses filepath.Match for glob patterns, first match wins"

patterns-established:
  - "secret: prefix convention in all YAML config for credential fields"
  - "Known-secret field enforcement: maintain list, fail if plaintext"
  - "Validate-then-swap: parse new config, validate, only swap if valid"
  - "fsnotify + 500ms debounce for config hot-reload"
  - "Go template rendering at check-in time (not load time) with .Facts/.Vars context"

requirements-completed: [SECR-01, SECR-02, SECR-03, SECR-04, SECR-05, SECR-06, SECR-07, CONF-01, CONF-02, CONF-03, CONF-04, CONF-05, CONF-06, CONF-07, CONF-08]

# Metrics
duration: 6min
completed: 2026-03-06
---

# Phase 1 Plan 2: Secrets & Config Summary

**Pluggable secrets provider with SecretString redaction, config loader with secret: resolution, fsnotify hot-reload, hostname-glob classification, and Go template rendering**

## Performance

- **Duration:** 6 min
- **Started:** 2026-03-06T00:59:58Z
- **Completed:** 2026-03-06T01:06:00Z
- **Tasks:** 2
- **Files modified:** 15

## Accomplishments
- Provider interface with Get/GetBytes/Close enables pluggable secret backends (StaticProvider now, IsopassProvider in Phase 3)
- SecretString never leaks secrets: String() returns [REDACTED], MarshalJSON/MarshalText/LogValue all redact
- StaticProvider converts key paths to env vars (isogov/db/dsn -> ISOGOV_DB_DSN) and hard-fails on missing keys
- ConfigStore loads per-client YAML, resolves secret: prefixes, enforces known-secret fields, and hot-reloads via fsnotify with validate-then-swap
- Classifier auto-assigns modules to clients by hostname glob pattern (first match wins, explicit config takes precedence)
- Template rendering supports .Facts and .Vars namespaces for check-in time rendering
- 19 tests total (8 secrets + 11 config) covering all behaviors

## Task Commits

Each task was committed atomically:

1. **Task 1: Secrets provider interface, SecretString, and StaticProvider** - `c9728db` (feat)
2. **Task 2: Config loader with secret resolution, classification, fsnotify, and templates** - `82f73b0` (feat)

_Note: Task 1 was committed as part of an earlier agent run that also included 01-04 work. The secrets implementation is identical to what this plan specifies._

## Files Created/Modified
- `server/secrets/provider.go` - Provider interface definition (Get, GetBytes, Close)
- `server/secrets/secret_string.go` - SecretString with [REDACTED] on all output paths
- `server/secrets/static.go` - StaticProvider reading .env via godotenv
- `server/secrets/static_test.go` - 8 tests for secrets package
- `server/config/types.go` - ClientConfig, ModuleConfig, ResourceConfig, ClassificationRule types
- `server/config/loader.go` - ConfigStore with loading, secret resolution, watcher, env merge
- `server/config/classifier.go` - Classifier with glob-based hostname matching
- `server/config/template.go` - RenderTemplate with .Facts/.Vars context
- `server/config/loader_test.go` - 11 tests for config package
- `config/server.yaml` - Server config with secret: prefixed DSN and TLS paths
- `config/clients/example.yaml` - Example client with modules, vars, env_vars
- `config/classification.yaml` - Example classification rules for web-*, db-*, staging

## Decisions Made
- Key path to env var mapping uses uppercase with slashes converted to underscores (isogov/db/dsn -> ISOGOV_DB_DSN) for StaticProvider
- Known-secret field list is a package-level map for easy extension; fields: dsn, tls_cert_path, tls_key_path, webhook_secret
- Unconfigured clients get a non-nil ClientConfig with empty Modules slice (allows callers to skip nil checks)
- Classification uses Go's filepath.Match for glob patterns (supports *, ?)

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
- Task 1 files were already committed in `c9728db` by a previous agent run. Verified the implementation matches the plan spec exactly, confirmed all 8 tests pass, and proceeded to Task 2 without re-committing.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Secrets Provider interface ready for consumption by server startup, mTLS setup, and DB connection
- ConfigStore ready for gRPC CheckIn handler to resolve client configs
- Classifier ready for auto-classification of enrolled clients
- Template renderer ready for check-in time rendering with agent facts
- IsopassProvider implementation deferred to Phase 3 per roadmap

## Self-Check: PASSED

All 12 source files verified present. Both task commits (c9728db, 82f73b0) confirmed in git log. All 19 tests pass (8 secrets + 11 config).

---
*Phase: 01-foundation*
*Completed: 2026-03-06*
