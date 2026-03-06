# Project Research Summary

**Project:** Isogov -- Puppet-style Configuration Management System
**Domain:** Pull-based configuration management (CM) for Linux/macOS fleets
**Researched:** 2026-03-05
**Confidence:** HIGH

## Executive Summary

Isogov is a pull-based configuration management system in the lineage of Puppet and Chef, built in Go with a YAML-only configuration language, gRPC/mTLS agent-server communication, integrated secrets management via isopass, and a single-binary deployment model. The research confirms this is a well-understood domain with established architectural patterns (catalog compilation, check-before-act idempotency, DAG-ordered resource convergence, fact-driven conditional configuration). The recommended technology stack is mature, high-confidence Go ecosystem tooling: gRPC + grpc-gateway for dual transport, pgx for PostgreSQL, Cobra for the CLI, slog + Prometheus for observability, and buf for proto compilation. No exotic or unproven technologies are required.

The recommended approach is to build from the foundation outward: proto definitions and database schema first, then the server skeleton with mTLS and enrollment, then the agent with fact collection and the pull loop, then the convergence engine (DAG, module executors, notify/subscribe), then operational features (compliance, dry-run, REST API, CLI), and finally observability integrations and the web UI. This order follows strict dependency chains identified in both the architecture and feature research. The isopass integration should use a StaticProvider initially to avoid blocking early development on an external dependency.

The primary risks are: (1) secret leakage through logs, errors, and gRPC payloads -- mitigated by implementing a `SecretString` type from day one, (2) thundering herd on server restart or force-push -- mitigated by server-side admission control and staggered force-push timestamps, (3) non-atomic file writes corrupting managed files -- mitigated by the write-temp-rename pattern, and (4) notify/subscribe creating hidden dependency loops -- mitigated by combined graph validation at config load time. All four are solvable with upfront design decisions rather than reactive fixes.

## Key Findings

### Recommended Stack

The stack is almost entirely Go stdlib plus established community libraries. No framework lock-in, no ORM, no external runtime dependencies in production. The agent and server are each single Go binaries. See `.planning/research/STACK.md` for full details.

**Core technologies:**
- **Go 1.24+**: Language runtime -- tool directive in go.mod for build tooling, post-quantum TLS support, mature ecosystem
- **gRPC + grpc-gateway**: Dual transport -- gRPC for agent communication (binary protocol, mTLS), REST auto-generated from proto annotations for CLI/UI
- **pgx v5**: PostgreSQL driver -- 10-20x faster than lib/pq via native interface, LISTEN/NOTIFY for real-time events, pgxpool for connection management
- **Cobra**: CLI framework -- 10+ subcommand model for gvctl, proven by kubectl/helm/docker
- **buf**: Proto toolchain -- replaces error-prone protoc CLI flags with config-driven compilation, linting, and managed mode
- **slog + Prometheus**: Observability -- zero-dependency structured logging (stdlib), official Prometheus client for metrics
- **fsnotify**: Config file watching -- only option in Go ecosystem, cross-platform, stable
- **Custom DAG (~100 LOC)**: Kahn's algorithm for topological sort -- too simple to justify an external dependency

**Key version pins:** Go >= 1.24.13, grpc-go >= 1.70, pgx >= 5.8, cobra >= 1.8, grpc-gateway >= 2.27.

**Notable decisions:** grpc-gateway over Connect-Go (annotation-based REST generation from single proto source of truth), pgx native over database/sql (binary protocol performance, PostgreSQL-specific features), custom DAG over gonum (100 lines vs massive dependency), React SPA embedded via go:embed (last priority, can be revisited).

### Expected Features

The feature landscape is well-mapped from Puppet, Chef, Ansible, and Salt. See `.planning/research/FEATURES.md` for full analysis including competitor matrix and dependency graph.

**Must have (table stakes):**
- File, package, service resource types with check-before-act idempotency
- DAG-ordered execution with circular dependency detection
- Notify/subscribe triggers (the package/file/service triad)
- gRPC/mTLS transport with node enrollment and one-time tokens
- Agent pull loop with jitter and exponential backoff
- Fact collection (OS, hostname, IP, architecture minimum)
- YAML config with Go template rendering and environment variable hierarchy
- isopass secret resolution (`secret:` prefix)
- Convergence reporting (what changed, what failed, what was skipped)
- gvctl CLI for basic operations (clients, runs, push)
- Dry-run/preview mode

**Should have (differentiators):**
- Single Go binary deployment (biggest DX advantage vs Puppet/Chef/Salt)
- Integrated secrets with auto-rotation awareness (novel -- no competitor does this)
- Built-in webhooks with HMAC signing (competitors gate behind enterprise editions)
- Built-in Prometheus metrics (competitors require exporters)
- File snapshots with CLI rollback
- Auto node classification from pattern rules
- Agent self-update from server

**Defer (v2+):**
- Cron/mount/host/SSH keys resources
- Multi-server HA
- RBAC
- Plugin/module system for custom resource types
- Cross-node orchestration

**Critical gap identified:** The spec lacks an exec/command resource. Every major CM tool has this as an escape hatch. Recommend adding to v1.x with `onlyif`/`unless`/`creates` guards for idempotency. Also: user resource should follow core file/package/service, not ship simultaneously.

### Architecture Approach

Classic hub-spoke: stateless server compiling catalogs from filesystem YAML, agents polling via gRPC/mTLS, PostgreSQL for all persistent state. Four deployable artifacts (server binary, agent binary, gvctl CLI, embedded web UI). Server exposes two listeners: gRPC (:9090) for agents, HTTP (:8080) for REST API + web UI + metrics. See `.planning/research/ARCHITECTURE.md` for full component diagram and data flow sequences.

**Major components:**
1. **Server Config Loader** -- reads/watches YAML files, resolves secrets, merges env vars; sole owner of config parsing
2. **Server Runner/Compiler** -- builds DAG from module declarations, topological sort, evaluates `when:` conditions against facts, produces ordered catalog
3. **Server Registry** -- enrollment, pattern matching, token validation, pubkey storage
4. **Agent Pull Loop** -- jittered check-in timer with exponential backoff, gRPC client reuse
5. **Agent Module Executors** -- check-before-act idempotent convergence for file/package/service/user with pluggable package manager backends
6. **Agent Fact Collectors** -- pluggable interface for OS, hardware, network, package inventory

**Key patterns to follow:** Provider interfaces for all external dependencies, check-before-act idempotency on every executor, filesystem as sole source of truth with watch + debounce, signed request authentication (ED25519 over payload + timestamp in gRPC interceptor), combined DAG + notify graph validation at load time.

### Critical Pitfalls

See `.planning/research/PITFALLS.md` for full analysis of 10 pitfalls plus integration gotchas, performance traps, and security mistakes.

1. **Non-atomic file writes** -- Use write-to-temp + fsync + chmod/chown + rename pattern. Never `os.WriteFile` directly to target path. Must be correct from first file executor implementation.
2. **Thundering herd on restart/force-push** -- Server-side semaphore for concurrent CheckIn processing, staggered force-push timestamps, agent startup jitter. Design into server skeleton from the start.
3. **Secret leakage** -- Implement `SecretString` type (returns `[REDACTED]` from Stringer/TextMarshaler) before any code resolves secrets. Nearly impossible to retrofit.
4. **Notify/subscribe loops** -- Merge dependency edges and notification edges into one graph, run cycle detection on the combined graph, cap cascade depth. Validate at config load time, not runtime.
5. **mTLS CA rotation breaks all agents** -- Support CA cert bundles (multiple PEMs), separate server cert rotation from CA rotation, document the rotation procedure. Design into agent TLS config from the start.

## Implications for Roadmap

Based on the dependency chains from FEATURES.md, the build order from ARCHITECTURE.md, the stack decisions from STACK.md, and the phase-to-pitfall mapping from PITFALLS.md, the following phase structure is recommended.

### Phase 1: Foundation (Proto, Schema, Secrets Interface, Server Skeleton)
**Rationale:** Everything depends on proto definitions, database schema, and the secrets provider interface. The architecture research identifies these as Layer 0 with zero dependencies. Getting proto and schema right early prevents cascading regeneration and migration headaches.
**Delivers:** Compilable proto with all messages/services, initial database migrations, `SecretString` type + `Provider` interface + `StaticProvider`, server binary that starts gRPC + HTTP listeners with mTLS, database connection pool via pgxpool.
**Addresses:** Secure transport foundation (table stakes), secret handling infrastructure.
**Avoids:** Secret leakage (Pitfall 3 -- `SecretString` type exists before any secret resolution code), database migration conflicts (Pitfall from technical debt patterns).

### Phase 2: Enrollment and Agent Skeleton
**Rationale:** The feature dependency graph shows enrollment is the gateway to all agent functionality. You cannot test the pull loop without enrolled clients. Registration before check-in is a hard dependency.
**Delivers:** Agent ED25519 keypair generation, Register RPC, server-side pattern matching + token validation + pubkey storage, agent config loading, basic gRPC client with connection reuse.
**Addresses:** Node enrollment/registration (table stakes), one-time tokens.
**Avoids:** Replay attacks (timestamp + nonce in signed payloads), insufficient token entropy (256-bit `crypto/rand` tokens), registration brute-force (rate limiting).

### Phase 3: Fact Collection and Config Loading
**Rationale:** Facts are required before catalog compilation (conditional resources evaluate against facts). Config loading is required before the runner can compile catalogs. These are the two inputs to the compiler.
**Delivers:** Pluggable Collector interface with OS/hardware/network/package fact collectors, YAML config loader with Go template rendering, environment variable hierarchy merging, `secret:` prefix resolution (via StaticProvider initially), fsnotify-based config watcher with debounce.
**Addresses:** Fact/inventory collection, template rendering, variable hierarchy, secret resolution (all table stakes).
**Avoids:** File watcher misses from atomic editor saves (Pitfall 6 -- watch directories + debounce), template injection via untrusted facts (Pitfall 7 -- parse-then-execute, restricted function map), config errors served to agents (validate on reload, serve last-known-good).

### Phase 4: Convergence Engine (DAG, Module Executors, Notify/Subscribe)
**Rationale:** This is the core value of the system. The architecture research places this at Layers 4-5 (depends on config loading + facts). The feature research identifies idempotent execution, DAG ordering, and notify/subscribe as the top three table-stakes convergence features. File, package, and service form the classic "triad" that must work together.
**Delivers:** DAG construction with topological sort and cycle detection, file executor (atomic writes, SHA256 check, snapshots), package executor (apt + dnf with non-interactive flags, retry on lock contention), service executor (systemctl start/stop/enable/disable + daemon-reload), notify/subscribe trigger chain, agent pull loop with jitter + backoff, CheckIn RPC with catalog delivery, ReportResult RPC with per-resource results.
**Addresses:** All convergence engine table stakes (idempotency, DAG, notify, file/package/service resources, convergence reporting).
**Avoids:** Non-atomic file writes (Pitfall 1), thundering herd (Pitfall 2 -- server semaphore, agent jitter), notify loops (Pitfall 4 -- combined graph validation), partial convergence damage (Pitfall 5 -- configurable fail-fast/continue, detailed skip reasons), package lock contention (Pitfall 9 -- retry + non-interactive flags).

### Phase 5: Operations (Compliance, Dry-Run, REST API, CLI)
**Rationale:** These are consumer-facing features that depend on the core convergence loop working. The architecture research places these at Layer 6. The feature research identifies dry-run and compliance as table stakes, and the CLI as the primary admin interface.
**Delivers:** Compliance status tracking (compliant/drifted/failing/stale/unknown), dry-run mode via DryRun RPC + plan computation, REST API via grpc-gateway (auto-generated from proto annotations), gvctl CLI with Cobra (clients, runs, push, preview, tokens, env, rollback subcommands), file rollback from snapshots.
**Addresses:** Compliance/drift status, dry-run/preview, CLI, run history, rollback (table stakes + differentiators).
**Avoids:** Opaque compliance status without resource details (UX pitfall -- include specific drifted resources), dry-run without diffs (UX pitfall -- include unified diff for file resources).

### Phase 6: Classification, User Resource, Conditional Resources
**Rationale:** These are v1.x features that add power after the core is proven. Auto-classification depends on facts being reliable. Conditional resources (`when:`) depend on template rendering being solid. User resource is the fourth core type but less critical than file/package/service.
**Delivers:** Auto node classification from `classification.yaml` pattern rules, `when:` expression evaluation on resources, user resource (create/modify/remove via /etc/passwd), exec resource with idempotency guards (onlyif/unless/creates).
**Addresses:** Auto classification, conditional resources, user management, exec escape hatch (v1.x features).
**Avoids:** Classification rule opacity (UX pitfall -- show matched rule in client detail).

### Phase 7: Integrations and Observability
**Rationale:** Webhooks, Prometheus, isopass real integration, and secret rotation awareness are differentiators but not launch blockers. The architecture research places these at Layer 7 specifically because the StaticProvider allows full development without isopass. Wiring in the real isopass integration requires reading its source and may involve debugging.
**Delivers:** Webhook dispatcher with HMAC signing + retries + circuit breaking, Prometheus metrics endpoint (client count, check-ins, drift, run duration, compliance ratios), real IsopassProvider replacing StaticProvider, secret rotation watcher with force-run triggers.
**Addresses:** Webhooks, metrics, secrets rotation awareness (differentiators).
**Avoids:** Webhook goroutine leaks (set timeout, circuit-break), isopass as bottleneck (cache secrets with TTL, invalidate on rotation).

### Phase 8: Web UI, Bootstrap, Agent Self-Update
**Rationale:** The web UI is purely a consumer of the REST API and adds no new server logic. Building it last ensures the API is stable. Bootstrap and self-update are operational conveniences. The architecture research explicitly places these at Layers 8-9.
**Delivers:** React SPA dashboard (clients, runs, compliance, config viewer) embedded via go:embed, curl bootstrap one-liner, agent self-update with checksum verification + rollback to previous binary.
**Addresses:** Web UI, bootstrap script, agent self-update (differentiators).
**Avoids:** Agent self-update bricking (Pitfall 10 -- two-phase update with self-test, keep previous binary, staged rollout).

### Phase 9: Packaging and Hardening
**Rationale:** Final phase before release. Dockerfiles, docker-compose for dev/test, Makefile for builds, additional package manager backends, load testing.
**Delivers:** Dockerfile.server, Dockerfile.agent, docker-compose.yml, Makefile, multi-platform package managers (apk, pacman, brew, zypper), load testing at 500+ simulated agents.
**Addresses:** Deployment packaging, broader OS support.

### Phase Ordering Rationale

- **Phases 1-4 form the critical path.** Every subsequent phase depends on the convergence engine working end-to-end. The dependency chain is: proto -> schema -> secrets interface -> server skeleton -> enrollment -> facts + config -> DAG + executors -> reporting. Cutting this chain anywhere blocks all downstream work.
- **Phase 5 (operations) before Phase 6 (classification/when) because** operators need visibility into what the system is doing before adding more complexity. Dry-run and compliance tracking are safety nets that make subsequent feature development safer.
- **Phase 7 (integrations) is intentionally late because** the StaticProvider pattern decouples all development from isopass availability. Wiring in real isopass is integration work that should happen once the core system is proven.
- **Phase 8 (web UI) is last because** it is the lowest-value feature for initial adoption (sysadmins prefer CLI) and the highest-cost (React SPA development). If the project needs to ship early, this phase can be deferred entirely.
- **This ordering avoids the top pitfalls by design:** SecretString exists before any resolution code (Phase 1), server admission control exists before agents connect (Phase 1-2), atomic file writes are built into the first file executor (Phase 4), combined graph validation is built into the first notify implementation (Phase 4).

### Research Flags

Phases likely needing deeper research during planning:
- **Phase 4 (Convergence Engine):** The most complex phase. DAG construction, notify/subscribe interaction with the dependency graph, and failure propagation semantics all need careful design. The Puppet documentation on resource relationships is extensive because this is where most CM tools have bugs.
- **Phase 7 (isopass Integration):** The isopass API is not publicly documented. Real integration requires reading the isopass source code. This phase should include a spike to understand the isopass API before building the provider.
- **Phase 8 (Agent Self-Update):** Self-update is uniquely dangerous (replacing the running binary). The two-phase update, self-test, and rollback mechanisms need careful design.

Phases with standard patterns (skip additional research):
- **Phase 1 (Foundation):** gRPC server setup, mTLS, pgx connection pools, proto compilation with buf -- all have extensive documentation and examples.
- **Phase 2 (Enrollment):** ED25519 key generation, bcrypt token hashing, gRPC client/server -- standard Go crypto and gRPC patterns.
- **Phase 3 (Facts/Config):** YAML parsing, Go template rendering, fsnotify -- all well-documented with clear best practices.
- **Phase 5 (Operations):** grpc-gateway REST generation, Cobra CLI, compliance state machines -- established patterns.

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | HIGH | All technologies are mature, actively maintained, and well-documented. Version requirements verified against current releases. Only MEDIUM item is go.yaml.in/yaml/v3 (new import path, fallback available). |
| Features | HIGH | Domain is 15+ years old (Puppet 2005, Chef 2009). Table stakes are unambiguous. Competitor analysis covers all major players. Feature dependency graph is validated against Puppet/Chef/Salt architectures. |
| Architecture | HIGH | Pull-based CM architecture is Puppet's model, documented extensively in academic and practitioner literature. Component boundaries follow the handoff spec. Build order derived from dependency analysis. |
| Pitfalls | HIGH | Pitfalls sourced from Puppet community bug reports, CM operational experience, Go-specific gotchas (fsnotify, template injection), and security cheat sheets (OWASP). All pitfalls have concrete prevention strategies. |

**Overall confidence:** HIGH

### Gaps to Address

- **isopass API:** No public documentation found. The interface between isogov and isopass must be discovered by reading isopass source. Use the `StaticProvider` to defer this until Phase 7, but plan a research spike before that phase.
- **Exec resource design:** Not in the original spec but identified as a critical gap by feature research. The idempotency guard model (`onlyif`/`unless`/`creates`) needs design work during Phase 6 planning.
- **Multi-platform testing:** The agent runs on Linux and macOS with different package managers, init systems, and filesystem behaviors. Integration testing strategy for cross-platform support needs definition during Phase 4 planning.
- **YAML library import path:** `go.yaml.in/yaml/v3` is the recommended active fork but the import path is new. If module resolution issues arise, fall back to `gopkg.in/yaml.v3` (archived but functional). Test the import during Phase 1.
- **Scalability ceiling:** Architecture is single-server. Research identifies ~10,000 nodes as the point where multi-server becomes necessary. If the target fleet exceeds this, HA planning should start in Phase 5 (not Phase 9).

## Sources

### Primary (HIGH confidence)
- [Puppet Architecture Overview](https://www.puppet.com/docs/puppet/5.5/architecture.html) -- pull-based catalog compilation model
- [The Architecture of Open Source Applications: Puppet](https://aosabook.org/en/v2/puppet.html) -- internal architecture reference
- [gRPC-Go releases and documentation](https://pkg.go.dev/google.golang.org/grpc) -- transport layer
- [jackc/pgx](https://github.com/jackc/pgx) -- database driver
- [grpc-gateway](https://github.com/grpc-ecosystem/grpc-gateway) -- REST API generation
- [Puppet Resource Type Reference](https://www.puppet.com/docs/puppet/7/type.html) -- feature baseline
- [OWASP Secrets Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html) -- security patterns
- [gRPC Security OWASP Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/gRPC_Security_Cheat_Sheet.html) -- transport security
- Isogov handoff document -- authoritative project specification

### Secondary (MEDIUM confidence)
- [Go 1.24 release notes](https://go.dev/doc/go1.24) -- language version features
- [CM tool comparisons (Veritis, DasRoot, TeachMeAnsible)](https://www.veritis.com/blog/chef-vs-puppet-vs-ansible-comparison-of-devops-management-tools/) -- feature landscape
- [Puppet dependency and relationship docs](https://www.puppet.com/docs/puppet/7/lang_relationships.html) -- notify/subscribe patterns
- [fsnotify documentation and known issues](https://pkg.go.dev/github.com/fsnotify/fsnotify) -- file watcher behavior
- [Hitless TLS Certificate Rotation in Go](https://blog.diogomonica.com/2017/01/11/hitless-tls-certificate-rotation-in-go/) -- mTLS patterns
- [Go SSTI / template injection risks](https://www.oligo.security/blog/safe-by-default-or-vulnerable-by-design-golang-server-side-template-injection) -- template security

### Tertiary (LOW confidence)
- [go.yaml.in/yaml/v3 migration discussions](https://github.com/go-task/task/issues/2171) -- YAML library status (active but young import path)

---
*Research completed: 2026-03-05*
*Ready for roadmap: yes*
