# Requirements: Isogov

**Defined:** 2026-03-05
**Core Value:** Managed nodes converge to their declared desired state reliably, idempotently, and securely — with full audit visibility into what changed, when, and why.

## v1 Requirements

### Transport & Security

- [ ] **SEC-01**: Server accepts only mTLS gRPC connections from agents — no plaintext
- [ ] **SEC-02**: Agents authenticate via ED25519 SSH keypair with signed payloads on every request
- [ ] **SEC-03**: Server verifies client signature against stored pubkey before processing any request
- [ ] **SEC-04**: Self-signed CA + server cert generation via `make certs` for development
- [ ] **SEC-05**: Production supports bring-your-own TLS certs by replacing cert files in volume

### Enrollment

- [ ] **ENRL-01**: Admin can add approved hostname/IP wildcard patterns with one-time tokens via gvctl
- [ ] **ENRL-02**: Agent registers by sending hostname, IP, pubkey, and one-time token to server
- [ ] **ENRL-03**: Server validates hostname/IP against approved patterns using glob matching
- [ ] **ENRL-04**: One-time tokens are bcrypt-hashed at rest and marked used after successful enrollment
- [ ] **ENRL-05**: Server returns assigned client UUID on successful registration
- [ ] **ENRL-06**: Admin can revoke a client via gvctl, blocking all future check-ins
- [ ] **ENRL-07**: Revoked clients cannot re-enroll with the same UUID

### Convergence Engine

- [ ] **CONV-01**: File resource executor checks SHA256 hash before writing, skips if match
- [ ] **CONV-02**: File resource uses atomic write-to-temp-then-rename pattern
- [ ] **CONV-03**: Package resource executor queries package manager for installed state before acting
- [ ] **CONV-04**: Service resource executor checks systemctl is-active/is-enabled before acting
- [ ] **CONV-05**: User resource executor parses /etc/passwd for existence and attributes before acting
- [ ] **CONV-06**: Exec resource executor runs arbitrary commands with idempotency guards (creates/onlyif/unless)
- [ ] **CONV-07**: Module dependencies form a DAG with topological ordering via Kahn's algorithm
- [ ] **CONV-08**: Circular dependencies detected at config load time with clear error message
- [ ] **CONV-09**: Resources support `notify` list — triggered resources re-apply when notifier changes
- [ ] **CONV-10**: Resources support `subscribes` — alternative syntax for notify relationships
- [ ] **CONV-11**: Service resource supports states: running, stopped, restarted, reloaded
- [ ] **CONV-12**: All executors record action_taken (none/create/update/delete/skip) in run results

### Configuration

- [ ] **CONF-01**: Server loads per-client config from `config/clients/*.yaml` files
- [ ] **CONF-02**: Module YAML files use Go template syntax with `.Facts` and `.Vars` namespaces
- [ ] **CONF-03**: Server watches `config/clients/` directory and reloads config without restart
- [ ] **CONF-04**: Environment tiers allow per-environment variable overrides in `env_vars` blocks
- [ ] **CONF-05**: Base vars merged with env-specific vars at render time (env wins on conflict)
- [ ] **CONF-06**: Auto-classification rules in `config/classification.yaml` assign modules by hostname/IP pattern
- [ ] **CONF-07**: Explicit client YAML takes precedence over classification rules
- [ ] **CONF-08**: Unconfigured clients receive empty module list and run cleanly with no changes
- [ ] **CONF-09**: Resources support `when:` field as Go template expression against facts
- [ ] **CONF-10**: Resources where `when` evaluates to false are skipped with action_taken: skipped

### Secrets

- [ ] **SECR-01**: `secrets.Provider` interface with `Get(ctx, key)` and `GetBytes(ctx, key)` methods
- [ ] **SECR-02**: `IsopassProvider` satisfies Provider interface using real isopass client
- [ ] **SECR-03**: `StaticProvider` reads from environment variables for local dev/testing
- [ ] **SECR-04**: Config loader detects `secret:` prefix and resolves via Provider at startup
- [ ] **SECR-05**: Known-secret fields without `secret:` prefix cause startup failure
- [ ] **SECR-06**: isopass unreachable at startup = hard failure, no insecure fallback
- [ ] **SECR-07**: Secret values never appear in logs, error messages, DB rows, gRPC payloads, or run results
- [ ] **SECR-08**: Server polls isopass for version changes on watched secret paths at configurable interval
- [ ] **SECR-09**: Secret rotation triggers immediate force-run for all affected clients
- [ ] **SECR-10**: Server TLS cert reloads without restart via `crypto/tls` `GetCertificate` callback

### Agent

- [ ] **AGNT-01**: Agent runs as daemon with jittered pull loop (base_interval ± jitter_percent)
- [ ] **AGNT-02**: Agent handles server unavailability with exponential backoff, never crashes
- [ ] **AGNT-03**: Agent collects facts: os.family/distro/version/kernel/arch, hardware.cpu_count/memory_mb/disk_gb, network.hostname/fqdn/ip_addresses/mac_addresses, packages.installed
- [ ] **AGNT-04**: Facts sent with every check-in, stored in DB, available as template variables
- [ ] **AGNT-05**: Pluggable fact collectors via `Collector` interface (Name, Collect)
- [ ] **AGNT-06**: Agent supports `--dry-run` flag for one-shot preview mode
- [ ] **AGNT-07**: Agent supports `isogov-agent run` for one-shot immediate convergence
- [ ] **AGNT-08**: Agent supports `run --module <name>` to apply a single module
- [ ] **AGNT-09**: File snapshots saved to `/var/lib/isogov/snapshots/` before overwrites (last 5 per path)
- [ ] **AGNT-10**: Agent checks for newer binary version on each check-in, self-updates after checksum verification
- [ ] **AGNT-11**: Agent generates ED25519 keypair on first run, stores private key locally

### Database

- [ ] **DB-01**: PostgreSQL 16 with golang-migrate for schema migrations
- [ ] **DB-02**: Tables: approved_patterns, clients, runs, run_results, client_facts, plans, plan_results, file_snapshots, secret_rotation_events, package_audit, webhooks
- [ ] **DB-03**: All tables use UUIDs as primary keys
- [ ] **DB-04**: Client facts retention: last 10 snapshots per client

### CLI (gvctl)

- [ ] **CLI-01**: `gvctl approved add/list/revoke` — manage approved patterns
- [ ] **CLI-02**: `gvctl clients list/show/revoke` — manage enrolled clients
- [ ] **CLI-03**: `gvctl push <host> / --all` — force immediate check-in
- [ ] **CLI-04**: `gvctl runs list/show` — view run history and detail
- [ ] **CLI-05**: `gvctl tokens generate` — generate token and print bootstrap curl command
- [ ] **CLI-06**: `gvctl webhooks add/list/delete` — manage webhook subscriptions
- [ ] **CLI-07**: `gvctl preview <host> / --all / --env` — trigger dry-run
- [ ] **CLI-08**: `gvctl rollback <host> --run/--file` — restore file snapshots
- [ ] **CLI-09**: `gvctl packages list/drift` — package version audit
- [ ] **CLI-10**: `gvctl env promote` — promote env_var overrides between tiers
- [ ] **CLI-11**: gvctl talks to server REST API, not gRPC directly

### Observability

- [ ] **OBS-01**: Compliance status per client: compliant/drifted/failing/stale/unknown
- [ ] **OBS-02**: Compliance API: summary, per-client status, 30-day history
- [ ] **OBS-03**: Prometheus metrics: clients_total, clients_active, checkins_total, run_duration_seconds, drift_detected_total, module_actions_total, compliance_status, compliance_pct, time_since_checkin_seconds
- [ ] **OBS-04**: Webhook events: run.success/failure/partial, drift.detected, client.enrolled/revoked, plan.created/changes_detected, rollback.success/failure, secret.rotated, compliance.degraded, package.version_drift, node.stale
- [ ] **OBS-05**: Webhook payloads signed with `X-Isogov-Signature: sha256=<hmac>`

### Web UI

- [ ] **UI-01**: Dashboard: total clients, active in last hour, recent failures, drift rate
- [ ] **UI-02**: Clients view: table with hostname, IP, enrolled date, last seen, status badge
- [ ] **UI-03**: Client detail: last N runs, expandable per-module results, drift history chart
- [ ] **UI-04**: Runs view: filterable run log across all clients
- [ ] **UI-05**: Config view: read-only display of client YAML config
- [ ] **UI-06**: Plans view: dry-run plans with per-resource actions, color-coded by severity
- [ ] **UI-07**: Compliance view: percentage gauge, breakdown donut, non-compliant table, 30-day trend
- [ ] **UI-08**: SPA served from server binary via `go:embed`

### Infrastructure

- [ ] **INFRA-01**: Docker Compose with server, postgres, isopass services
- [ ] **INFRA-02**: Dockerfile.server and Dockerfile.agent
- [ ] **INFRA-03**: Makefile: build, proto, certs, test, lint targets
- [ ] **INFRA-04**: Bootstrap script at `GET /bootstrap` with server address + CA cert baked in
- [ ] **INFRA-05**: Agent binary download endpoint: `GET /api/v1/agent/download/{os}/{arch}`
- [ ] **INFRA-06**: Multi-platform package manager support via PackageManager interface (apt, dnf, yum, apk, pacman, brew, zypper)

## v2 Requirements

### Extended Resources

- **EXT-01**: Group resource type (separate from user)
- **EXT-02**: Cron resource type for scheduled job management
- **EXT-03**: Plugin/module system for community-contributed resource types

### Advanced Operations

- **ADV-01**: Multi-server scaling with shared DB
- **ADV-02**: Environment promotion audit trail in database

## Out of Scope

| Feature | Reason |
|---------|--------|
| Push-based execution | Pull model only — agents behind firewalls/NAT; `gvctl push` sets DB flag |
| Agent-to-agent communication | Agents only talk to the server |
| Windows support | Linux and macOS only |
| Non-Go agent implementations | Single Go binary for all platforms |
| GUI-based config editing | Config is YAML files on disk, edited directly |
| Real-time chat/streaming | Check-in polling model is sufficient |
| Custom DSL | Go templates + YAML is the config language |

## Traceability

| Requirement | Phase | Status |
|-------------|-------|--------|
| SEC-01 | Phase 1 | Pending |
| SEC-02 | Phase 1 | Pending |
| SEC-03 | Phase 1 | Pending |
| SEC-04 | Phase 1 | Pending |
| SEC-05 | Phase 1 | Pending |
| ENRL-01 | Phase 1 | Pending |
| ENRL-02 | Phase 1 | Pending |
| ENRL-03 | Phase 1 | Pending |
| ENRL-04 | Phase 1 | Pending |
| ENRL-05 | Phase 1 | Pending |
| ENRL-06 | Phase 1 | Pending |
| ENRL-07 | Phase 1 | Pending |
| DB-01 | Phase 1 | Pending |
| DB-02 | Phase 1 | Pending |
| DB-03 | Phase 1 | Pending |
| DB-04 | Phase 1 | Pending |
| SECR-01 | Phase 1 | Pending |
| SECR-02 | Phase 1 | Pending |
| SECR-03 | Phase 1 | Pending |
| SECR-04 | Phase 1 | Pending |
| SECR-05 | Phase 1 | Pending |
| SECR-06 | Phase 1 | Pending |
| SECR-07 | Phase 1 | Pending |
| CONF-01 | Phase 1 | Pending |
| CONF-02 | Phase 1 | Pending |
| CONF-03 | Phase 1 | Pending |
| CONF-04 | Phase 1 | Pending |
| CONF-05 | Phase 1 | Pending |
| CONF-06 | Phase 1 | Pending |
| CONF-07 | Phase 1 | Pending |
| CONF-08 | Phase 1 | Pending |
| AGNT-11 | Phase 1 | Pending |
| INFRA-01 | Phase 1 | Pending |
| INFRA-02 | Phase 1 | Pending |
| INFRA-03 | Phase 1 | Pending |
| CONV-01 | Phase 2 | Pending |
| CONV-02 | Phase 2 | Pending |
| CONV-03 | Phase 2 | Pending |
| CONV-04 | Phase 2 | Pending |
| CONV-05 | Phase 2 | Pending |
| CONV-06 | Phase 2 | Pending |
| CONV-07 | Phase 2 | Pending |
| CONV-08 | Phase 2 | Pending |
| CONV-09 | Phase 2 | Pending |
| CONV-10 | Phase 2 | Pending |
| CONV-11 | Phase 2 | Pending |
| CONV-12 | Phase 2 | Pending |
| CONF-09 | Phase 2 | Pending |
| CONF-10 | Phase 2 | Pending |
| AGNT-01 | Phase 2 | Pending |
| AGNT-02 | Phase 2 | Pending |
| AGNT-03 | Phase 2 | Pending |
| AGNT-04 | Phase 2 | Pending |
| AGNT-05 | Phase 2 | Pending |
| AGNT-06 | Phase 2 | Pending |
| AGNT-07 | Phase 2 | Pending |
| AGNT-08 | Phase 2 | Pending |
| AGNT-09 | Phase 2 | Pending |
| INFRA-06 | Phase 2 | Pending |
| CLI-01 | Phase 3 | Pending |
| CLI-02 | Phase 3 | Pending |
| CLI-03 | Phase 3 | Pending |
| CLI-04 | Phase 3 | Pending |
| CLI-05 | Phase 3 | Pending |
| CLI-06 | Phase 3 | Pending |
| CLI-07 | Phase 3 | Pending |
| CLI-08 | Phase 3 | Pending |
| CLI-09 | Phase 3 | Pending |
| CLI-10 | Phase 3 | Pending |
| CLI-11 | Phase 3 | Pending |
| OBS-01 | Phase 3 | Pending |
| OBS-02 | Phase 3 | Pending |
| OBS-03 | Phase 3 | Pending |
| OBS-04 | Phase 3 | Pending |
| OBS-05 | Phase 3 | Pending |
| SECR-08 | Phase 3 | Pending |
| SECR-09 | Phase 3 | Pending |
| SECR-10 | Phase 3 | Pending |
| UI-01 | Phase 4 | Pending |
| UI-02 | Phase 4 | Pending |
| UI-03 | Phase 4 | Pending |
| UI-04 | Phase 4 | Pending |
| UI-05 | Phase 4 | Pending |
| UI-06 | Phase 4 | Pending |
| UI-07 | Phase 4 | Pending |
| UI-08 | Phase 4 | Pending |
| AGNT-10 | Phase 4 | Pending |
| INFRA-04 | Phase 4 | Pending |
| INFRA-05 | Phase 4 | Pending |

**Coverage:**
- v1 requirements: 89 total
- Mapped to phases: 89
- Unmapped: 0

---
*Requirements defined: 2026-03-05*
*Last updated: 2026-03-05 after roadmap creation*
