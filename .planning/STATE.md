---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: in-progress
stopped_at: Completed 04-02-PLAN.md (Bootstrap & Agent Self-Update)
last_updated: "2026-03-06T20:17:46Z"
last_activity: 2026-03-06 -- Completed plan 04-02 (Bootstrap & Agent Self-Update)
progress:
  total_phases: 4
  completed_phases: 3
  total_plans: 18
  completed_plans: 17
  percent: 94
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-03-05)

**Core value:** Managed nodes converge to their declared desired state reliably, idempotently, and securely -- with full audit visibility into what changed, when, and why.
**Current focus:** Phase 4: Web UI & Deployment -- In Progress

## Current Position

Phase: 4 of 4 (Web UI & Deployment)
Plan: 2 of 3 in current phase
Status: In Progress
Last activity: 2026-03-06 -- Completed plan 04-02 (Bootstrap & Agent Self-Update)

Progress: [█████████ ] 94%

## Performance Metrics

**Velocity:**
- Total plans completed: 13
- Average duration: 5min
- Total execution time: 0.81 hours

**By Phase:**

| Phase | Plans | Total | Avg/Plan |
|-------|-------|-------|----------|
| 01-foundation | 5/5 | 27min | 5min |
| 02-convergence | 5/5 | 28min | 6min |

**Recent Trend:**
- Last 5 plans: 02-02 (9min), 02-03 (5min), 02-05 (5min), 03-01 (2min), 03-03 (3min)
- Trend: steady

*Updated after each plan completion*
| Phase 02 P03 | 5min | 2 tasks | 15 files |
| Phase 02 P04 | 4min | 2 tasks | 4 files |
| Phase 02 P05 | 5min | 2 tasks | 5 files |
| Phase 03 P01 | 2min | 2 tasks | 27 files |
| Phase 03 P04 | 5min | 2 tasks | 12 files |
| Phase 03 P03 | 3min | 2 tasks | 14 files |
| Phase 03 P02 | 6min | 2 tasks | 7 files |
| Phase 03 P05 | 4min | 2 tasks | 10 files |
| Phase 04 P02 | 5min | 2 tasks | 9 files |

## Accumulated Context

### Decisions

Decisions are logged in PROJECT.md Key Decisions table.
Recent decisions affecting current work:

- [Roadmap]: Coarse granularity -- 4 phases compressing research's 9 phases into Foundation / Convergence / Operations / Web UI & Deployment
- [Roadmap]: StaticProvider used for secrets in Phase 1-2; real IsopassProvider + rotation watcher deferred to Phase 3
- [Roadmap]: Classification rules (CONF-06/07) grouped with config loading in Phase 1, not deferred
- [01-01]: Disabled buf lint PACKAGE_DIRECTORY_MATCH and SERVICE_SUFFIX to keep flat proto/isogov.proto layout
- [01-01]: Proto output to gen/proto/isogov/ for clean Go import paths
- [01-01]: Updated buf lint from deprecated DEFAULT to STANDARD category
- [01-03]: Used filepath.Match for glob matching (stdlib, covers hostname and IP patterns per spec)
- [01-03]: Glob-first filtering before bcrypt comparison to avoid timing leaks
- [01-03]: One-time token enforcement via ListUnusedApprovedPatterns query filter
- [01-03]: ENRL-07 enforced by checking GetClientByHostname for revoked status before pattern matching
- [01-04]: Payload signing concatenates payload+timestamp bytes for simplicity
- [01-04]: PKCS8 PEM for private key, PKIX PEM for public key (x509 stdlib compatible)
- [01-02]: Key path to env var: isogov/db/dsn -> ISOGOV_DB_DSN (uppercase, slashes to underscores)
- [01-02]: Known-secret fields (dsn, tls_cert_path, tls_key_path, webhook_secret) reject plaintext
- [01-02]: Unconfigured clients receive empty module list (non-nil config)
- [01-02]: Classification uses filepath.Match for glob patterns, first match wins
- [01-05]: Signature payload for CheckIn uses state_fingerprint, timestamp as Unix epoch string
- [01-05]: Register returns success=false for no-matching-pattern, PermissionDenied for revoked hostname
- [01-05]: Gateway reuses server TLS cert as mTLS client to gRPC backend
- [01-05]: CheckIn renders templates with empty facts map (facts reported separately)
- [01-05]: Seed command uses ON CONFLICT DO NOTHING for idempotent re-runs
- [Phase 02-03]: gopsutil promoted from indirect to direct dependency for cross-platform fact collection
- [Phase 02-03]: PackageCollector returns empty map on unsupported platforms (non-fatal, informational)
- [Phase 02-03]: CheckIn falls through to empty facts map when no facts exist yet (first check-in)
- [02-01]: Snapshot naming uses SHA256 hex of file path as prefix, runID, .bak suffix
- [02-01]: EvaluateWhen uses missingkey=zero so missing facts render as zero values
- [02-01]: Only exact string "true" after trim evaluates as true in when expressions
- [02-01]: CommandRunner interface added for testable command execution in executors
- [02-02]: ExecExecutor guard order: creates (file stat) first, then onlyif, then unless
- [02-02]: Exec guards (onlyif/unless) skipped during dry-run to avoid side effects; creates guard still evaluated
- [02-02]: Shared mockCmdRunnerGeneric in mocks_test.go avoids duplicating test mock infrastructure
- [Phase 02]: [02-04]: Module-level depends_on/notify/subscribes resolved to resource names during BuildDAG
- [Phase 02]: [02-04]: Notify map kept separate from sorted list for post-execution processing
- [Phase 02]: [02-04]: Notify pass skipped entirely during dry-run to avoid side effects
- [02-05]: RunResult struct wraps results + server timing for clean propagation from RunOnce to Loop
- [02-05]: ReportFacts accepts map[string]any and marshals to JSON internally
- [02-05]: Register command wired with actual gRPC call, saves client UUID to data-dir/client.uuid
- [02-05]: Package manager auto-detected via OS facts with graceful fallback
- [03-01]: Updated all existing client SELECT queries to include force_push column to avoid partial struct issues
- [03-01]: ListAllRuns added to runs.sql rather than a new file for query locality
- [03-04]: promauto for metric registration (auto-registers with DefaultRegisterer)
- [03-04]: Webhook retries use configurable linear backoff (attempt * retryBaseDelay)
- [03-04]: Dispatcher nil-checked before dispatch calls to allow optional webhook support
- [03-01]: ComplianceSnapshot uses DATE type with UNIQUE constraint for daily aggregation
- [03-03]: Shared apiClient struct with get/post/delete methods and 30s timeout for all CLI REST calls
- [03-03]: Separate Go file per CLI subcommand group with init() registration pattern
- [03-03]: ANSI color in preview command degrades gracefully for non-terminal stdout
- [03-03]: rollback requires exactly one of --run or --file (mutually exclusive validation)
- [Phase 03]: RunInfo struct decouples compliance from db.Run; stale=2x baseInterval; ForcePush sets DB flag only
- [03-05]: Interface-based watcher deps (VersionProvider, ClientStore, DB, EventDispatcher) for clean unit testing
- [03-05]: CertCache with sync.RWMutex for concurrent TLS handshake reads; NewServer returns 3 values including CertCache
- [04-02]: text/template for bootstrap script (not html/template) to avoid HTML escaping in shell
- [04-02]: Single-quoted interpolated values in bootstrap script for shell injection prevention
- [04-02]: Checksum cache keyed by file path with modtime invalidation
- [04-02]: ExecFn injection pattern for testable syscall.Exec without replacing process in tests
- [04-02]: UpdateConfig struct passed to Loop for optional self-update (nil disables)
- [04-02]: Self-update failure logged as warning, never crashes agent (retry next cycle)

### Pending Todos

None yet.

### Blockers/Concerns

- isopass API must be understood by reading source before Phase 3 secrets rotation work
- Multi-platform package manager testing strategy needed before Phase 2 package executor work

## Session Continuity

Last session: 2026-03-06T20:17:46Z
Stopped at: Completed 04-02-PLAN.md (Bootstrap & Agent Self-Update)
Resume file: .planning/phases/04-web-ui-deployment/04-02-SUMMARY.md
