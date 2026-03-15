# Gap Analysis

A comparison of Hobnob against mature configuration management systems (Puppet, Ansible, Chef, SaltStack), identifying remaining gaps for production readiness.

Last updated: 2026-03-14

## Status Summary

**20 original gaps identified. 16 resolved. 4 deferred.**

| # | Gap | Severity | Status |
|---|-----|----------|--------|
| 1 | Rate limiting on check-in RPC | Critical | Resolved — token-bucket per-thrall + global |
| 2 | Maintenance windows / change freeze | Critical | Resolved — per-thrall + global freeze_until |
| 3 | Config change attribution | Critical | Resolved — publish audit trail with operator, diffs |
| 4 | Resource execution timeouts | Critical | Resolved — per-resource timeout via runner |
| 5 | Horizontal scaling | Critical | **Deferred** — federation covers geographic distribution |
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

---

## Deferred Gaps

### 5. Horizontal scaling

Single server with in-memory FleetIndex. No distributed locking, no read replicas, no shared state between instances. Federation (hub-spoke) solves geographic distribution but not single-site HA. The [scalability analysis](scalability.md) shows a single server handles 50K-200K thralls comfortably.

### 8. Per-resource rollback beyond files

File changes are rolled back via FileTransactionTracker on mid-convergence failure. But failed package installs, user creation, service state changes, and cron entries are not reverted. A partial failure can leave the system in an inconsistent state.

### 13. LVM / storage management

No executors for logical volumes, volume groups, physical volumes, partitions, or RAID arrays. Block device trait collectors provide visibility but not management. Workaround requires exec resources.

### 20. Phased rollout beyond rolling execution

Rolling execution supports batch sizing and health checks, but no multi-stage canary (10% → 50% → 100%), no blue-green config deployment, no time-gated promotion, and no automatic rollback if canary batch fails.

---

## Resolved Since Initial Analysis

All resolved items were implemented between 2026-03-12 and 2026-03-14.

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

## Lower Priority (Unchanged)

### UI and CLI

- No UI bulk actions (select multiple thralls for bulk operations)
- No web-based config editor (operators edit .hnb files on disk)
- No topology visualization (no visual DAG for depends_on/notify relationships)
- No real-time event stream (HTMX auto-refresh, no SSE/WebSocket)

### Agent

- No systemd unit creation executor (service manages existing units; creating requires file + service)
- No package pinning/hold (version specification works, no apt hold / yum versionlock)
- No agent health self-check (no detection of stuck convergence)

### Documentation

- ~~No migration guides~~ — Resolved: 5 guides (Puppet, Ansible, Chef, Salt, CFEngine)
- ~~No scaling guide~~ — Resolved: [Scalability Analysis](scalability.md)
- No chaos or resilience tests

### Testing

- No E2E integration tests (cross-binary harness exists but no full workflow tests)
- No UI tests (no browser automation)
- No load tests (benchmarks exist but no documented large-scale scenario)
