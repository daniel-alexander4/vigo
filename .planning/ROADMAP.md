# Roadmap: Isogov

## Overview

Isogov delivers a pull-based configuration management system where agents converge managed nodes to declared desired state via gRPC/mTLS. The roadmap moves from server foundation (transport, enrollment, config loading, database) through the core convergence engine (DAG-ordered idempotent executors, agent daemon, fact collection) into operational tooling (CLI, compliance, observability) and finally the web UI and deployment conveniences. Each phase delivers a coherent, independently verifiable capability.

## Phases

**Phase Numbering:**
- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

- [x] **Phase 1: Foundation** - Server skeleton with mTLS, enrollment, config loading, secrets interface, and database
- [ ] **Phase 2: Convergence** - Agent daemon with DAG-ordered idempotent resource executors, fact collection, and notify/subscribe triggers
- [ ] **Phase 3: Operations** - CLI, REST API, compliance tracking, secret rotation, webhooks, and Prometheus metrics
- [ ] **Phase 4: Web UI & Deployment** - Web dashboard SPA, bootstrap script, and agent self-update

## Phase Details

### Phase 1: Foundation
**Goal**: A running server that accepts mTLS connections, enrolls agents via one-time tokens, loads per-client YAML config with template rendering and secret resolution, and persists all state to PostgreSQL
**Depends on**: Nothing (first phase)
**Requirements**: SEC-01, SEC-02, SEC-03, SEC-04, SEC-05, ENRL-01, ENRL-02, ENRL-03, ENRL-04, ENRL-05, ENRL-06, ENRL-07, DB-01, DB-02, DB-03, DB-04, SECR-01, SECR-02, SECR-03, SECR-04, SECR-05, SECR-06, SECR-07, CONF-01, CONF-02, CONF-03, CONF-04, CONF-05, CONF-06, CONF-07, CONF-08, AGNT-11, INFRA-01, INFRA-02, INFRA-03
**Success Criteria** (what must be TRUE):
  1. Server starts and accepts only mTLS gRPC connections -- plaintext connections are rejected
  2. An agent can generate an ED25519 keypair, register with a valid one-time token against an approved hostname pattern, and receive a client UUID
  3. Revoked clients receive errors on check-in and cannot re-enroll with the same UUID
  4. Server loads per-client YAML config from disk, resolves `secret:` prefixes via the Provider interface, merges environment variable overrides, applies classification rules, and hot-reloads on file changes
  5. `make proto`, `make certs`, `make build`, and `docker compose up` all work to produce a running development environment with server, PostgreSQL, and isopass
**Plans**: 5 plans

Plans:
- [x] 01-01-PLAN.md -- Proto, DB migrations, sqlc, infrastructure (Makefile, Docker, certs)
- [x] 01-02-PLAN.md -- Secrets provider interface, StaticProvider, config loading with hot-reload and classification
- [x] 01-03-PLAN.md -- Enrollment registry: pattern matching, token validation, revocation
- [x] 01-04-PLAN.md -- Agent stub: ED25519 keypair generation, signing, registration client
- [x] 01-05-PLAN.md -- Server wiring: mTLS gRPC, REST gateway, handlers, integration

### Phase 2: Convergence
**Goal**: Agents pull their config from the server, build a dependency DAG, and idempotently converge managed resources (files, packages, services, users, exec) with notify/subscribe triggers, conditional execution, fact-driven templates, and full result reporting
**Depends on**: Phase 1
**Requirements**: CONV-01, CONV-02, CONV-03, CONV-04, CONV-05, CONV-06, CONV-07, CONV-08, CONV-09, CONV-10, CONV-11, CONV-12, CONF-09, CONF-10, AGNT-01, AGNT-02, AGNT-03, AGNT-04, AGNT-05, AGNT-06, AGNT-07, AGNT-08, AGNT-09, INFRA-06
**Success Criteria** (what must be TRUE):
  1. Agent runs as a daemon with jittered pull loop, checks in with the server, receives its compiled catalog, and executes resources in topological order -- circular dependencies are rejected at load time
  2. File, package, service, user, and exec resources all check current state before acting and skip when already converged -- every action is recorded as none/create/update/delete/skip
  3. Changing a file resource triggers a service restart via notify/subscribe, and resources with `when:` expressions that evaluate to false are skipped
  4. Agent collects OS, hardware, network, and package facts via pluggable collectors, sends them with every check-in, and facts are available as template variables in config rendering
  5. Agent supports `--dry-run` for preview, `run` for one-shot convergence, `run --module <name>` for single-module apply, and saves file snapshots before overwrites
**Plans**: 5 plans

Plans:
- [ ] 02-01-PLAN.md -- Executor interface, file executor with SHA256 + atomic write, snapshot manager, when: evaluator
- [ ] 02-02-PLAN.md -- PackageManager interface (7 backends), package/service/user/exec executors
- [ ] 02-03-PLAN.md -- Fact collectors (OS/hardware/network/packages), server ReportResult/ReportFacts handlers
- [ ] 02-04-PLAN.md -- DAG topological sort (Kahn's), runner with notify/subscribe triggers
- [ ] 02-05-PLAN.md -- Agent daemon loop with jitter/backoff, gRPC client, CLI commands (run/dry-run/module)

### Phase 3: Operations
**Goal**: Operators have full visibility and control over the fleet via gvctl CLI and REST API, with compliance tracking, dry-run previews, file rollback, webhook notifications, Prometheus metrics, and automated secret rotation
**Depends on**: Phase 2
**Requirements**: CLI-01, CLI-02, CLI-03, CLI-04, CLI-05, CLI-06, CLI-07, CLI-08, CLI-09, CLI-10, CLI-11, OBS-01, OBS-02, OBS-03, OBS-04, OBS-05, SECR-08, SECR-09, SECR-10
**Success Criteria** (what must be TRUE):
  1. `gvctl` can manage approved patterns, list/show/revoke clients, force push, view runs, generate tokens, manage webhooks, trigger dry-run previews, rollback file snapshots, audit packages, and promote environment variables -- all via REST API
  2. Each client has a compliance status (compliant/drifted/failing/stale/unknown) with 30-day history accessible via API
  3. Prometheus metrics endpoint exposes client counts, check-in rates, drift detection, run duration, and compliance percentages
  4. Webhook events fire for run results, drift detection, enrollment changes, secret rotation, compliance degradation, and package drift -- all payloads signed with HMAC
  5. Server polls isopass for secret version changes at configurable intervals and triggers immediate force-runs for affected clients; TLS certs reload without server restart
**Plans**: TBD

Plans:
- [ ] 03-01: TBD
- [ ] 03-02: TBD
- [ ] 03-03: TBD

### Phase 4: Web UI & Deployment
**Goal**: Operators can monitor the fleet through a web dashboard, and new nodes can be bootstrapped with a single curl command and kept up to date via agent self-update
**Depends on**: Phase 3
**Requirements**: UI-01, UI-02, UI-03, UI-04, UI-05, UI-06, UI-07, UI-08, AGNT-10, INFRA-04, INFRA-05
**Success Criteria** (what must be TRUE):
  1. Web UI dashboard shows total clients, active count, recent failures, and drift rate -- with drill-down to individual client runs, module results, and drift history
  2. Web UI provides filterable run log, read-only config viewer, dry-run plan viewer with color-coded actions, and compliance gauge with 30-day trend chart
  3. `curl <server>/bootstrap | bash` installs the agent binary, configures the server address and CA cert, generates a keypair, and enrolls the node
  4. Agent checks for newer binary versions on each check-in and self-updates after checksum verification
**Plans**: TBD

Plans:
- [ ] 04-01: TBD
- [ ] 04-02: TBD

## Progress

**Execution Order:**
Phases execute in numeric order: 1 -> 2 -> 3 -> 4

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Foundation | 5/5 | Complete | 2026-03-06 |
| 2. Convergence | 0/5 | Planned | - |
| 3. Operations | 0/3 | Not started | - |
| 4. Web UI & Deployment | 0/2 | Not started | - |
