# Phase 1: Foundation - Context

**Gathered:** 2026-03-05
**Status:** Ready for planning

<domain>
## Phase Boundary

Server skeleton that accepts mTLS gRPC connections, enrolls agents via approved patterns + one-time tokens, loads per-client YAML config with template rendering and secret resolution, and persists all state to PostgreSQL. Includes Docker Compose, Makefile, and cert generation. Agent binary stub for keypair generation and registration only (no convergence loop yet).

</domain>

<decisions>
## Implementation Decisions

### Proto & API Design
- Single `isogov.proto` file — all services and messages in one file
- Buf for proto tooling — lint, format, breaking-change detection, code generation
- grpc-gateway to generate REST API from proto annotations — one source of truth for gRPC and REST
- Separate ports: gRPC on 9090 (agent traffic), REST on 8080 (gvctl, web UI, metrics)

### Config Loading
- fsnotify with debounce (~500ms) for detecting config file changes
- Validation before swap: bad config is logged with error, last-known-good stays active. Server never runs invalid config.
- In-memory map (RWMutex-guarded) keyed by hostname for compiled configs
- Go templates rendered at check-in time (not config load time) — always uses latest facts and vars

### Secrets Bootstrapping
- StaticProvider reads from `.env` file (gitignored) for local dev/testing
- Both layers of enforcement: convention-based `secret:` prefix resolution + strict enforcement on known-secret fields (dsn, TLS keys, webhook secrets). Startup fails if a known-secret field lacks the prefix.
- `SecretString` wrapper type with `String()` returning `[REDACTED]` — prevents accidental logging via fmt/slog
- Hard fail with clear error message when a `secret:` reference can't be resolved. Log which path failed, exit immediately.

### DB & Migrations
- sqlc for type-safe query generation — write SQL, get Go code
- Sequential migration numbering: `000001_create_approved_patterns.up.sql`
- Dev seed data via `make seed` — inserts test clients, approved patterns, sample data for faster iteration
- testcontainers-go for integration tests — real PostgreSQL in Docker per test suite

### Claude's Discretion
- Exact fsnotify debounce duration (500ms is a starting point)
- sqlc configuration details (sqlc.yaml structure)
- Buf module configuration (buf.yaml, buf.gen.yaml)
- grpc-gateway annotation style
- In-memory config map concurrency implementation details
- Docker Compose networking and health checks
- Makefile target structure beyond the required ones (build, proto, certs, test, lint, seed)

</decisions>

<specifics>
## Specific Ideas

- The handoff spec at `isogov-handoff.md` contains exact proto service definitions, DB schema SQL, config file formats, and Docker Compose structure — follow these closely
- Research recommended pgx v5 as the PostgreSQL driver (lib/pq is in maintenance mode)
- Research recommended slog (stdlib) for logging over zerolog/zap
- Research recommended cobra for the CLI framework
- StaticProvider for secrets in Phase 1; real IsopassProvider deferred to Phase 3 (per roadmap decision)

</specifics>

<code_context>
## Existing Code Insights

### Reusable Assets
- None — greenfield project

### Established Patterns
- None — this phase establishes all foundational patterns

### Integration Points
- `isogov-handoff.md` at repo root contains the full specification
- `~/repos/isopass` contains the secrets backend source (read before Phase 3, not needed for Phase 1's StaticProvider)

</code_context>

<deferred>
## Deferred Ideas

None — discussion stayed within phase scope

</deferred>

---

*Phase: 01-foundation*
*Context gathered: 2026-03-05*
