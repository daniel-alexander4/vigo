# Gap Analysis

A comparison of Hobnob against mature configuration management systems (Puppet, Ansible, Chef, SaltStack), identifying remaining gaps for production readiness.

Last updated: 2026-03-13

---

## Critical

### 1. No rate limiting on check-in RPC

All thralls can call `CheckIn()` simultaneously with no per-thrall or global throttle. A thundering herd (e.g., after server restart or network recovery) could overwhelm the server. No backpressure mechanism exists -- thralls retry with backoff on the agent side, but the server has no knob to shape inbound load.

### 2. No maintenance windows or change freeze

No way to:

- Pause convergence runs on selected thralls
- Block config publishes during a window
- Schedule deferred config changes (e.g., "apply at 11pm")
- Mark a thrall as "maintenance: scheduled"

Every mature CM tool provides maintenance windows. Without this, operators cannot safely coordinate changes with planned downtime.

### 3. No config change attribution

The filesystem (`hnb/proposed/`) is the config source of truth, but there is no record of who changed what or when. `hobnobcli config publish` does not log the operator. Git integration is recommended but not enforced or built-in. No config version history exists beyond the current `.live/` snapshot.

### 4. No resource execution timeouts

A hung `exec` command (interactive prompt, network hang, DNS timeout) blocks the entire convergence cycle indefinitely. There is no per-resource timeout parameter. `duration_ms` is recorded after the fact but never enforced as a limit. Affects all executors that shell out (`exec`, `archive`, custom executors).

### 5. No horizontal scaling

Single server with in-memory `FleetIndex`. No story for running multiple `hobd` instances:

- No distributed locking for concurrent config publishes
- No read replicas or eventual-consistency model
- No shared state between server instances
- Federation (hub-spoke) solves geographic distribution but not single-site HA

---

## High Severity

### 6. Config rollback is file-only

`hobnobcli rollback` reverts individual file snapshots from previous runs, not "rollback to previous published config version." There is no config snapshot history, no version-based rollback, no batch rollback across thralls, and no rollback validation (test on canary first).

### 7. No node tagging or classification

Targeting uses hostname glob patterns and trait filters. There is no explicit tag/label system for grouping nodes (e.g., "canary", "region-us-east", "maintenance", "batch-2"). Mature tools provide dynamic groups, node classifiers, and tag-based targeting.

### 8. No per-resource rollback beyond files

`FileTransactionTracker` rolls back file changes when a resource fails mid-convergence. But failed package installs, user creation, service state changes, and cron entries are not reverted. A partial failure can leave the system in an inconsistent state.

### 9. No drift alerting

Compliance status is computed and displayed in the UI, but there is no proactive notification when nodes drift. SMTP notifications exist for `thrall.enrolled`, `run.failure`, `drift.detected`, and `secret.rotated`, but not for fleet-level compliance threshold breaches (e.g., "fleet dropped below 95% compliant"). No Slack or PagerDuty integration.

### 10. No audit log CLI or UI

Task and workflow dispatch records the operator in the DB (`task_runs.operator`, `workflow_runs.operator`), but:

- No `hobnobcli audit` command to query the audit trail
- No audit log page in the web UI
- No filtering by operator, timestamp, or action type
- Config publish events are not audited at all

### 11. Environment promotion is audit-only

`PromoteEnv` RPC records the event in the `env_promotions` table but does not actually sync config files between environments. There are no approval gates, no scheduled promotions, and no automatic diff-and-apply workflow. Promotion is effectively manual file copying.

---

## Medium Severity

### 12. No SELinux context enforcement

SELinux traits are collected (`agent-rs/src/traits/selinux.rs`) but there is no executor to set or enforce security contexts. Operators must use `exec` with `chcon`/`semanage` commands, losing idempotency guarantees. AppArmor profile management is similarly absent.

### 13. No LVM or storage management

No executors for logical volumes, volume groups, physical volumes, partitions, or RAID arrays. Block device trait collectors provide visibility but not management. Puppet provides `physical_volume`, `volume_group`, `logical_volume` types. Workaround requires `exec` resources.

### 14. No certificate lifecycle management

No executor for certificate generation, renewal, or expiry validation. No ACME/Let's Encrypt integration. No cert expiry trait collector. Operators must manage certificates via `exec` + `file` resources with manual renewal tracking.

### 15. No listening ports trait

Cannot detect which services are listening on which ports. Critical for:

- Security audit conditionals (e.g., `when: "port_listening(22)"`)
- Compliance checks (unexpected open ports)
- Service dependency validation

Workaround requires custom trait collection via shell.

### 16. No fuzz testing

Config YAML parsing, `when:` expression evaluation, and trait processing are fuzz-worthy attack surfaces with zero fuzz coverage. Go's `testing/fuzz` and Rust's `cargo-fuzz` / `afl` are available but not used.

### 17. No OpenAPI spec

REST API has no machine-readable schema (OpenAPI/Swagger). Makes client SDK generation, API documentation, and third-party integration harder. gRPC has proto definitions but REST endpoints are hand-coded.

### 18. No multi-node impact analysis

`hobnobcli preview` shows planned changes for one node at a time. No way to ask "if I change module X, which nodes are affected and what will change on each?" No dependency impact graph. Operators must manually trace module-to-role-to-node mappings.

### 19. No line-level config diff

Config publish (`hobnobcli config publish`) reports changed and removed files via SHA-256 comparison, but does not show what changed inside each file. Operators see "nodes.hnb changed" but not "line 42: added web-03.example.com." No semantic diff.

### 20. No phased rollout beyond rolling execution

Rolling execution supports batch sizing and health checks, but:

- No multi-stage canary (10% then 50% then 100%)
- No blue-green config deployment
- No time-gated promotion between stages
- No automatic rollback if canary batch fails
- No shadow mode (deploy to subset, collect metrics, then promote)

---

## Lower Priority

### UI and CLI

- **No UI bulk actions** -- Cannot select multiple thralls for bulk revoke, tag, or push operations
- **No web-based config editor** -- Operators must edit `.hnb` files on disk; no in-browser YAML editor
- **No topology visualization** -- No visual DAG showing `depends_on`/`notify`/`subscribes` relationships between resources
- **No real-time event stream** -- UI polls via HTMX auto-refresh; no SSE or WebSocket for live updates
- **No RBAC-gated UI elements** -- Admin/viewer roles exist but most pages do not conditionally hide restricted actions

### Agent

- **No systemd unit creation executor** -- `service` executor manages existing units only; creating unit files requires `file` + `service` two-step
- **No package pinning/hold** -- Version specification works but no apt hold / yum versionlock mechanism
- **No agent health self-check** -- No detection of stuck convergence, no local health endpoint, no recovery from executor hangs
- **No running container inventory trait** -- Docker trait shows daemon presence but not running container details (image, status, ports)
- **No package update availability trait** -- Cannot detect which installed packages have updates pending

### Documentation

- **No migration guides** -- No documented path for teams moving from Puppet, Ansible, Chef, or Salt to Hobnob
- **No scaling guide** -- No documented fleet size limits, memory-per-thrall estimates, or performance benchmarks under load
- **No chaos or resilience tests** -- No tests for network partition, agent crash recovery, server restart during task dispatch, or concurrent config publish

### Testing

- **No E2E integration tests** -- Cross-binary test harness exists but no full workflow tests (enroll, configure, converge, verify)
- **No UI tests** -- No browser automation (Selenium, Playwright) for web UI workflows
- **No load tests** -- Benchmarks exist (16 total) but no documented load test scenario (e.g., 10k simultaneous check-ins)
