# Isogov

## What This Is

A Puppet-style configuration management system written in Go. A central server holds desired-state YAML configs for each managed node. Agents on those nodes pull their config via gRPC over mTLS, idempotently converge to desired state (files, packages, services, users), and report results back. An admin CLI (`gvctl`) and web UI provide operational visibility and control. All secrets are sourced exclusively from isopass — never stored in config files, env vars, or the database.

## Core Value

Managed nodes converge to their declared desired state reliably, idempotently, and securely — with full audit visibility into what changed, when, and why.

## Requirements

### Validated

(None yet — ship to validate)

### Active

- [ ] gRPC service with mTLS (Register, CheckIn, ReportResult, ForcePush, DryRun, Rollback, ReportFacts)
- [ ] ED25519 SSH keypair client authentication with signed payloads
- [ ] Client enrollment via approved hostname/IP patterns + one-time bcrypt-hashed tokens
- [ ] PostgreSQL backend with golang-migrate migrations
- [ ] YAML-based per-client config with `secret:` prefix resolution via isopass
- [ ] Idempotent module executors: file (with SHA256 check), package (multi-platform), service (systemctl), user (/etc/passwd)
- [ ] Module dependency DAG with topological ordering and circular dependency detection
- [ ] Notify/subscribe triggers between resources (e.g., restart service when config changes)
- [ ] Structured conditionals via `if:`/`not_if:` with trait-based operators on resources, blocks, and module references
- [ ] Environment tiers with per-environment variable overrides merged at render time
- [ ] Agent daemon with jittered pull loop, exponential backoff on server unavailability
- [ ] Agent fact collection (OS, hardware, network, packages) via pluggable Collector interface
- [ ] File snapshots before overwrites with rollback support via gvctl
- [ ] Dry-run / preview mode (agent + gvctl + DryRun RPC)
- [ ] Auto node classification from `config/classification.yaml` pattern rules
- [ ] Compliance status tracking (compliant/drifted/failing/stale/unknown) with history
- [ ] REST API for gvctl and web UI
- [ ] `gvctl` CLI: approved, clients, push, runs, tokens, webhooks, packages, env, preview, rollback
- [ ] Webhook dispatcher with HMAC signing for run/drift/enrollment/rotation events
- [ ] Prometheus metrics (clients, check-ins, drift, run duration, compliance)
- [ ] Secrets provider abstraction wrapping isopass (IsopassProvider + StaticProvider for dev)
- [ ] Server-side secret rotation watcher polling isopass with force-run on rotation
- [ ] Server TLS hot reload via `GetCertificate` callback
- [ ] Multi-platform package manager support (apt, dnf, yum, apk, pacman, brew, zypper)
- [ ] Package version pinning with drift detection and audit
- [ ] Agent self-update from server-hosted binary endpoint with checksum verification
- [ ] Bootstrap via server-hosted curl one-liner script
- [ ] Web UI SPA (dashboard, clients, runs, config, plans, compliance)
- [ ] Docker Compose for server + postgres + isopass; Makefile with build/proto/certs/test/lint targets
- [ ] Single config path: filesystem YAML is sole source of truth, server watches for changes

### Out of Scope

- Push-based execution (server initiating outbound connections to agents) — pull model only, `gvctl push` sets a DB flag
- Agent-to-agent communication — agents only talk to the server
- Windows support — Linux and macOS only
- Non-Go agent implementations — single Go binary for all platforms
- GUI-based config editing — config is YAML files on disk, edited directly

## Current Milestone: v1.2 Structured Conditionals

**Goal:** Replace freeform `when:` Go template expressions with structured `if:`/`not_if:` conditionals that validate exclusively against traits.

**Target features:**
- Conditional vars with trait-based operators (`only`, `not`, `gt`, `lt`, `gte`, `lte`, `match`)
- `if:` / `not_if:` on individual resources — replaces `when:`
- `conditional_block:` — inline resource grouping with shared conditionals
- Module-level `if:` / `not_if:` on module references in roles and clients.yaml

## Current State

**Shipped:** v1.0 MVP (2026-03-06), v1.1 Traits Visibility & Custom Traits (2026-03-07)
**Codebase:** ~23,400 LOC Go, 6 phases, 23 plans executed

**v1.1 delivered:**
- `isogov-agent traits` CLI for local trait inspection with dot-path accessor
- `gvctl clients traits` + REST endpoint for remote trait viewing
- Server-distributed custom trait executables with SHA256 sync, 10s timeout execution, and `custom.*` namespace merging

## Context

- Full specification is in `isogov-handoff.md` at the repo root — it contains proto definitions, DB schema, config formats, module syntax, and detailed behavioral requirements
- isopass source lives at `~/repos/isopass` — must be read and understood before implementing the secrets provider
- Design philosophy emphasizes simplicity (every concept explainable in one sentence), clarity (sysadmins can read YAML and immediately understand), and conciseness (no boilerplate, sensible defaults)

## Constraints

- **Language**: Go (latest stable) — non-negotiable
- **Transport**: gRPC over mTLS — no plaintext connections ever
- **Secrets**: isopass is the sole secrets backend — unreachable = hard startup failure
- **Database**: PostgreSQL 16 with golang-migrate
- **Auth**: ED25519 SSH keypairs, signature-verified on every request
- **Idempotency**: All module executors must check-before-act, never apply blindly
- **Tokens**: One-time tokens bcrypt-hashed at rest, never stored plaintext
- **Revocation**: Revoked clients get errors on check-in, cannot re-enroll with same UUID
- **Config path**: Filesystem YAML only — no API/DB/CLI/UI config writes

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Pull-based architecture (not push) | Agents behind firewalls/NAT; server doesn't need to reach clients | ✓ Good |
| isopass as sole secrets backend | Centralized secret management with rotation awareness | ✓ Good |
| YAML config on filesystem as source of truth | GitOps-friendly, auditable, no config drift between DB and files | ✓ Good |
| Go template syntax for module templates | Built into Go stdlib, familiar to Go developers, no extra dependency | ✓ Good |
| DAG-based module ordering | Explicit dependency declaration prevents ordering bugs | ✓ Good |
| Custom traits as executables (not plugins) | Language-agnostic, simple contract (JSON stdout), no Go SDK needed | ✓ Good |
| Manifest in CheckInResponse (not separate RPC) | One round-trip, no extra complexity | ✓ Good |

---
*Last updated: 2026-03-07 after v1.2 milestone started*
