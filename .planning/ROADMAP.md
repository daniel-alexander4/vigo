# Roadmap: Isogov

## Milestones

- [x] **v1.0 MVP** - Phases 1-4 (shipped 2026-03-06)
- [ ] **v1.1 Traits Visibility & Custom Traits** - Phases 5-6 (in progress)

## Phases

<details>
<summary>v1.0 MVP (Phases 1-4) - SHIPPED 2026-03-06</summary>

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
- [x] 01-01: Proto, DB migrations, sqlc, infrastructure (Makefile, Docker, certs)
- [x] 01-02: Secrets provider interface, StaticProvider, config loading with hot-reload and classification
- [x] 01-03: Enrollment registry: pattern matching, token validation, revocation
- [x] 01-04: Agent stub: ED25519 keypair generation, signing, registration client
- [x] 01-05: Server wiring: mTLS gRPC, REST gateway, handlers, integration

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
- [x] 02-01: File executor with SHA256 + atomic write, snapshot manager, when evaluator
- [x] 02-02: PackageManager interface (7 backends), package/service/user/exec executors
- [x] 02-03: Fact collectors (OS/hardware/network/packages), server ReportResult/ReportFacts handlers
- [x] 02-04: DAG topological sort (Kahn's), runner with notify/subscribe triggers
- [x] 02-05: Agent daemon loop with jitter/backoff, gRPC client, CLI commands (run/dry-run/module)

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
**Plans**: 5 plans

Plans:
- [x] 03-01: REST API for clients, runs, compliance, approved patterns
- [x] 03-02: gvctl push, preview, rollback, packages, env commands
- [x] 03-03: gvctl approved, clients, runs, tokens, webhooks commands
- [x] 03-04: Prometheus metrics + webhook dispatcher
- [x] 03-05: Secret rotation watcher + TLS hot reload

### Phase 4: Web UI & Deployment
**Goal**: Operators can monitor the fleet through a web dashboard, and new nodes can be bootstrapped with a single curl command and kept up to date via agent self-update
**Depends on**: Phase 3
**Requirements**: UI-01, UI-02, UI-03, UI-04, UI-05, UI-06, UI-07, UI-08, AGNT-10, INFRA-04, INFRA-05
**Success Criteria** (what must be TRUE):
  1. Web UI dashboard shows total clients, active count, recent failures, and drift rate -- with drill-down to individual client runs, module results, and drift history
  2. Web UI provides filterable run log, read-only config viewer, dry-run plan viewer with color-coded actions, and compliance gauge with 30-day trend chart
  3. `curl <server>/bootstrap | bash` installs the agent binary, configures the server address and CA cert, generates a keypair, and enrolls the node
  4. Agent checks for newer binary versions on each check-in and self-updates after checksum verification
**Plans**: 4 plans

Plans:
- [x] 04-01: UI package foundation (embed, renderer, CSS, HTMX), dashboard page, compliance page
- [x] 04-02: Bootstrap script endpoint, agent binary distribution, agent self-update mechanism
- [x] 04-03: Remaining UI pages (clients, client detail, runs, config viewer, plans viewer)
- [x] 04-04: Gap closure (bootstrap wiring + loop test fix)

</details>

### v1.1 Traits Visibility & Custom Traits (In Progress)

**Milestone Goal:** Make traits inspectable from CLI (agent + gvctl) and support server-distributed custom trait collectors.

- [ ] **Phase 5: Trait Inspection** - Agent and gvctl commands to view collected traits
- [ ] **Phase 6: Custom Traits** - Server-distributed trait executables with agent-side execution and merging

## Phase Details

### Phase 5: Trait Inspection
**Goal**: Admins can inspect collected traits for any node -- locally on the agent and remotely via gvctl
**Depends on**: Phase 4 (v1.0 complete)
**Requirements**: INSP-01, INSP-02, INSP-03
**Success Criteria** (what must be TRUE):
  1. Running `isogov-agent traits` on a managed node prints all collected traits to stdout as pretty-printed JSON, with optional collector filtering and dot-path accessor
  2. Running `gvctl clients traits <hostname>` prints the stored traits for that client from the server database
  3. The server REST endpoint `GET /api/v1/clients/{hostname}/traits` returns stored traits as a JSON envelope
**Plans**: 2 plans

Plans:
- [ ] 05-01-PLAN.md -- Agent `isogov-agent traits` subcommand with collector filtering and dot-path accessor
- [ ] 05-02-PLAN.md -- Server REST endpoint + `gvctl clients traits` subcommand

### Phase 6: Custom Traits
**Goal**: Admins can drop executable scripts into a server directory and have them automatically distributed to agents, executed with a timeout, and their output merged into the traits map under `custom.*`
**Depends on**: Phase 5
**Requirements**: CUST-01, CUST-02, CUST-03, CUST-04
**Success Criteria** (what must be TRUE):
  1. Placing an executable in the server's custom traits directory causes it to appear in the check-in manifest with its name and SHA256 checksum
  2. Agents compare the manifest against their local cache, download new or changed executables via a dedicated gRPC call, and store them locally
  3. Custom trait output appears under `custom.<name>` in the traits map and is usable in `{{ .Traits.custom.<name>.key }}` templates and `when:` expressions
  4. A failing or timed-out custom trait executable logs a warning and is skipped -- other traits and convergence proceed unaffected
**Plans**: 2 plans

Plans:
- [ ] 06-01-PLAN.md -- Proto definitions, server traits scanner, manifest in CheckIn, DownloadCustomTrait RPC
- [ ] 06-02-PLAN.md -- Agent sync/cache, execution with timeout, JSON parsing, traits merging in loop

## Progress

**Execution Order:**
Phases execute in numeric order: 5 -> 6

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|----------------|--------|-----------|
| 1. Foundation | v1.0 | 5/5 | Complete | 2026-03-06 |
| 2. Convergence | v1.0 | 5/5 | Complete | 2026-03-06 |
| 3. Operations | v1.0 | 5/5 | Complete | 2026-03-06 |
| 4. Web UI & Deployment | v1.0 | 4/4 | Complete | 2026-03-06 |
| 5. Trait Inspection | v1.1 | 0/2 | Planning | - |
| 6. Custom Traits | v1.1 | 0/2 | Planning | - |
