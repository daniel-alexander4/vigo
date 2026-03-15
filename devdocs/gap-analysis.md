# Gap Analysis

A comparison of Hobnob against mature configuration management systems (Puppet, Ansible, Chef, SaltStack), identifying remaining gaps for production readiness.

Last updated: 2026-03-15

## Status Summary

**26 gaps identified. 23 resolved. 3 deferred.**

| # | Gap | Severity | Status |
|---|-----|----------|--------|
| 1 | Rate limiting on check-in RPC | Critical | Resolved — token-bucket per-thrall + global |
| 2 | Maintenance windows / change freeze | Critical | Resolved — per-thrall + global freeze_until |
| 3 | Config change attribution | Critical | Resolved — publish audit trail with operator, diffs |
| 4 | Resource execution timeouts | Critical | Resolved — per-resource timeout via runner |
| 5 | Horizontal scaling / HA | Critical | Partially resolved — spanner + auto-failover. P2P design doc written |
| 6 | Config rollback | High | Resolved — timestamped snapshots, version-based rollback |
| 7 | Node tagging / classification | High | Resolved — tags on thralls, tag-based targeting |
| 8 | Per-resource rollback beyond files | High | **Deferred** |
| 9 | Drift alerting | High | Resolved — compliance threshold monitoring + SMTP |
| 10 | Audit log CLI / UI | High | Resolved — audit trail with filtering |
| 11 | Environment promotion | High | Resolved — environment_overrides in config |
| 12 | SELinux context enforcement | Medium | Resolved — selinux executor |
| 13 | LVM / storage management | Medium | **Deferred** |
| 14 | Certificate lifecycle | Medium | Resolved — cert_expiry trait collector |
| 15 | Listening ports trait | Medium | Resolved — ports trait collector |
| 16 | Fuzz testing | Medium | Resolved — Go fuzz targets added |
| 17 | OpenAPI spec | Medium | Resolved — generated from REST handlers |
| 18 | Multi-node impact analysis | Medium | Resolved — hobnobcli preview with pattern targeting |
| 19 | Line-level config diff | Medium | Resolved — publish pipeline shows unified diffs |
| 20 | Phased rollout | Medium | **Deferred** — rolling execution covers batch + health check |
| 21 | Web UI authentication | High | Resolved — basic auth default, OIDC, isowebauth |
| 22 | Admin UI for server management | High | Resolved — admin section with 6 pages |
| 23 | Spanner config sync | High | Resolved — hub pushes config to spokes on publish |
| 24 | Spanner auto-failover | High | Resolved — hub auto-drains unhealthy spokes |
| 25 | Spanner spoke→hub reporting | Medium | Resolved — spokes push status via hub_addr |
| 26 | Backup management via web UI | Medium | Resolved — snapshot creation + browser download |

---

## Deferred Gaps

### 8. Per-resource rollback beyond files

File changes are rolled back via FileTransactionTracker on mid-convergence failure. But failed package installs, user creation, service state changes, and cron entries are not reverted. A partial failure can leave the system in an inconsistent state.

### 13. LVM / storage management

No executors for logical volumes, volume groups, physical volumes, partitions, or RAID arrays. Block device trait collectors provide visibility but not management. Workaround requires exec resources.

### 20. Phased rollout beyond rolling execution

Rolling execution supports batch sizing and health checks, but no multi-stage canary (10% → 50% → 100%), no blue-green config deployment, no time-gated promotion, and no automatic rollback if canary batch fails.

---

## Resolved Since Last Update (2026-03-15)

### Web UI Authentication (Gap 21)

Authentication is always required. Three pluggable methods: basic (default, username/password with bcrypt hashes in secrets store), OIDC, isowebauth. Admin account auto-seeded on first startup. Sessions backed by SQLite (SCS). API tokens (`hbt_` prefix, bcrypt at rest). Two roles: admin/viewer. Localhost CLI requests trusted with implicit admin access (peer auth model).

### Admin UI (Gap 22)

Dedicated admin area with its own layout and sidebar navigation. Six admin pages:
- **Users** — account CRUD, role management
- **Enrollment** — approved pattern management
- **Tokens** — bootstrap token generation, API token CRUD
- **Webhooks** — webhook endpoint management
- **Health** — system diagnostics (database, TLS, secrets, license, config, stale thralls)
- **Backup** — status display, on-demand snapshot creation, browser download

Top bar with user identity and pill-shaped logout button across all pages.

### Spanner Config Sync (Gap 23)

Hub automatically pushes `.live/` config directory to all spokes on config publish. Uses `SyncConfig` gRPC RPC with tar.gz payload. Best-effort per spoke — partial failures logged but don't block publish. Path traversal protection on archive extraction.

### Spanner Auto-Failover (Gap 24)

Background goroutine on hub periodically probes spoke health (default 30s). After configurable consecutive failures (default 3 = 90s), automatically drains all thralls to other healthy spokes based on hostname pattern matching. Spokes marked healthy again on recovery. Configurable via `spanner.auto_failover` in server.yaml.

### Spanner Spoke Reporting (Gap 25)

Spokes proactively push compliance metrics to hub via `ReportSpokeStatus` gRPC RPC (every 30s). Uses the previously unused `hub_addr` config field. Hub merges self-reports with probe results — if a spoke is unreachable but has a recent report, last-known compliance data is preserved. Graceful degradation: spoke operates normally if hub unreachable.

### Backup via Web UI (Gap 26)

Two new REST endpoints: `POST /api/v1/backup/snapshot` (create archive) and `GET /api/v1/backup/download` (stream tar.gz to browser). Admin-only. Includes database, config, secrets, TLS certs, and server.yaml. Restore remains CLI-only (requires server restart).

### Additional Improvements

- **FHS compliance**: daemon binaries moved from `/usr/local/bin` to `/usr/local/sbin` (hobnob, hobd, hobnob-license-manager). CLI stays at `/usr/local/bin/hobnobcli`.
- **Package timeout**: default increased from 60s to 300s for package and repository executors (arm64/low-powered hardware was timing out during apt installs).
- **P2P spanner architecture document**: design doc for future peer-to-peer spanner eliminating hub as SPOF. Covers git-based config, peer discovery, leader election. Not implemented — hub-spoke sufficient for current use.

---

## Resolved Prior to 2026-03-15

All items below were implemented between 2026-03-12 and 2026-03-14.

**Critical (4/5 resolved):**
- Rate limiting: token-bucket interceptor, per-thrall + global, configurable in server.yaml
- Maintenance windows: per-thrall `maintenance_until` + global `freeze_until` config publish block
- Config attribution: `config_publishes` DB table with operator, changed files, diffs; REST API + CLI
- Execution timeouts: runner enforces per-resource timeout via thread::scope + channel (Rust agent)

**High (5/6 resolved):**
- Config rollback: `SnapshotLive()` after every publish, `RollbackToVersion()` via CLI/API, 20 versions retained
- Node tagging: `tags` field on thralls, `hobnobcli nodes tag/untag`, tag-based targeting in tasks/queries
- Drift alerting: `ComplianceMonitor` checks thresholds every metrics interval, fires SMTP on breach
- Audit log: unified audit trail for config publishes, task/workflow dispatch; CLI `hobnobcli audit list`
- Environment promotion: `environment_overrides` in node configs, per-environment variable overrides

**Medium (7/9 resolved):**
- SELinux executor: manages file contexts via `semanage fcontext` + `restorecon`
- Certificate expiry trait: `cert_expiry` collector reports days-until-expiry for PEM files
- Listening ports trait: `ports` collector reports TCP/UDP listeners with PID
- Fuzz testing: Go fuzz targets for YAML parsing and when-expression evaluation
- OpenAPI spec: auto-generated from REST handler registration
- Multi-node impact analysis: `hobnobcli preview --pattern "*.web.*"` shows changes across matching nodes
- Line-level config diff: publish pipeline computes and displays unified diffs for all changed files

---

## Lower Priority (Remaining)

### UI

- No bulk actions (select multiple thralls for bulk operations)
- No web-based config editor (operators edit .hnb files on disk)
- No topology visualization (no visual DAG for depends_on/notify relationships)
- No real-time event stream (HTMX auto-refresh, no SSE/WebSocket)

### Agent

- No agent health self-check (no detection of stuck convergence)

### Testing

- No E2E integration tests (cross-binary harness exists but no full workflow tests)
- No UI tests (no browser automation)
- No load tests (benchmarks exist but no documented large-scale scenario)
- No chaos or resilience tests
