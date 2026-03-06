# Architecture Patterns

**Domain:** Pull-based configuration management system (Puppet-style)
**Researched:** 2026-03-05

## Recommended Architecture

Isogov follows the classic pull-based configuration management model pioneered by Puppet and Chef: a central server holds desired-state declarations, agents on managed nodes poll for their catalog, idempotently converge, and report results back. The system has four deployable artifacts (server, agent, CLI, web UI) communicating over two transport layers (gRPC for agent-server, REST for human-facing tools).

### High-Level System Diagram

```
                      +--------------------+
                      |   Admin Workstation |
                      |                    |
                      |  gvctl CLI         |
                      +--------+-----------+
                               | REST/HTTPS
                               v
+----------------+    +--------+-----------+    +------------------+
| YAML Config    |    |                    |    |                  |
| (filesystem)   +--->+   Isogov Server    +--->+   PostgreSQL 16  |
|                |    |                    |    |                  |
| - server.yaml  |    | - gRPC server      |    +------------------+
| - clients/*.yaml|   | - REST API server  |
| - classification|   | - Config loader    |    +------------------+
| - modules/*.yaml|   | - Registry         |    |                  |
+----------------+    | - Runner/DAG       +--->+   isopass         |
                      | - Webhook dispatch |    |   (secrets)       |
                      | - Compliance track |    |                  |
                      | - Secret watcher   |    +------------------+
                      | - Classifier       |
                      | - Web UI (embedded)|    +------------------+
                      |                    |    |   Prometheus      |
                      +-----+---------+----+--->+   (metrics)       |
                            |         |         +------------------+
                      gRPC/mTLS  gRPC/mTLS
                            |         |
                   +--------+--+  +---+--------+
                   |  Agent 1  |  |  Agent N   |
                   |           |  |            |
                   | - Pull    |  | - Pull     |
                   | - Facts   |  | - Facts    |
                   | - Modules |  | - Modules  |
                   | - Report  |  | - Report   |
                   +-----------+  +------------+
```

### Component Boundaries

| Component | Responsibility | Communicates With | Transport |
|-----------|---------------|-------------------|-----------|
| **Server: gRPC service** | Agent registration, check-in dispatch, result collection, rollback/dry-run coordination | Agents | gRPC over mTLS |
| **Server: REST API** | Operations interface for CLI and Web UI (clients, runs, tokens, webhooks, compliance, packages, bootstrap) | gvctl CLI, Web UI, Prometheus | HTTPS |
| **Server: Config Loader** | Reads and watches YAML files from filesystem, resolves `secret:` prefixes, merges env vars | isopass (secrets), filesystem | Local filesystem + isopass API |
| **Server: Registry** | Enrollment: pattern matching, token validation, pubkey storage, revocation | PostgreSQL | SQL |
| **Server: Runner/DAG** | Builds dependency graph from module declarations, topological sort, dispatches ordered module list to agents | Config Loader, PostgreSQL | Internal |
| **Server: Classifier** | Auto-assigns modules to nodes without explicit config via pattern rules in `classification.yaml` | Config Loader | Internal |
| **Server: Compliance Tracker** | Computes per-client compliance status from run results, tracks history | PostgreSQL | Internal |
| **Server: Webhook Dispatcher** | Fires HMAC-signed HTTP POST to configured URLs on events | External webhook receivers | HTTPS |
| **Server: Secret Watcher** | Polls isopass for version changes on watched paths, queues force-runs on rotation | isopass, PostgreSQL | isopass API |
| **Server: Metrics** | Exposes Prometheus gauges/counters/histograms at `/metrics` | Prometheus | HTTP |
| **Server: Web UI** | SPA embedded via `go:embed`, served by REST API server | REST API | HTTP (same-origin) |
| **Agent: Auth** | ED25519 keypair generation, registration flow, payload signing | Server gRPC | gRPC over mTLS |
| **Agent: Pull Loop** | Jittered check-in timer, exponential backoff on failure | Server gRPC | gRPC over mTLS |
| **Agent: Fact Collectors** | Pluggable system data gathering (OS, hardware, network, packages) | Local system | OS APIs, /proc, package managers |
| **Agent: Module Executors** | Idempotent resource convergence (file, package, service, user) with check-before-act | Local system | OS commands, filesystem |
| **Agent: Snapshot Manager** | Pre-change file backup, rollback restoration | Local filesystem | Filesystem |
| **Agent: Package Manager Backends** | Abstracted install/remove/upgrade across apt/dnf/yum/apk/pacman/brew/zypper | Local package managers | OS commands |
| **gvctl CLI** | Admin operations: tokens, approvals, push, runs, rollback, preview, packages, env, webhooks | Server REST API | HTTPS |
| **PostgreSQL** | Persistent state: clients, runs, results, facts, plans, snapshots, webhooks, compliance, audit | Server | SQL (libpq) |
| **isopass** | Secret storage and retrieval, rotation signaling | Server, Agent (indirectly via server-resolved secrets in configs) | isopass API (to be determined from source) |

### Data Flow

#### 1. Agent Registration (First Run)

```
Agent                          Server                        PostgreSQL
  |                              |                              |
  |-- Generate ED25519 keypair   |                              |
  |                              |                              |
  |-- Register(hostname, IP,     |                              |
  |   pubkey, token) ----------->|                              |
  |                              |-- Match hostname/IP against  |
  |                              |   approved_patterns -------->|
  |                              |<-- Pattern match result -----|
  |                              |                              |
  |                              |-- Verify token (bcrypt) ---->|
  |                              |<-- Token valid, mark used ---|
  |                              |                              |
  |                              |-- Store client record ------>|
  |                              |<-- Client UUID --------------|
  |                              |                              |
  |<-- RegisterResponse(UUID) ---|                              |
  |                              |                              |
  |-- Store UUID locally         |                              |
```

#### 2. Check-In / Convergence Loop (Steady State)

```
Agent                          Server                        PostgreSQL    isopass     Filesystem
  |                              |                              |            |            |
  |-- Collect facts (OS,         |                              |            |            |
  |   hardware, network, pkgs)   |                              |            |            |
  |                              |                              |            |            |
  |-- CheckIn(UUID, facts,       |                              |            |            |
  |   state fingerprint) ------->|                              |            |            |
  |                              |-- Verify ED25519 signature ->|            |            |
  |                              |-- Store facts (JSONB) ------>|            |            |
  |                              |                              |            |            |
  |                              |-- Load client config --------|------------|----------->|
  |                              |   (or classify if no         |            |            |
  |                              |    explicit config)          |            |            |
  |                              |                              |            |            |
  |                              |-- Resolve secret: prefixes --|----------->|            |
  |                              |-- Merge env_vars overrides   |            |            |
  |                              |-- Evaluate when: conditions  |            |            |
  |                              |   against facts              |            |            |
  |                              |-- Build resource DAG,        |            |            |
  |                              |   topological sort           |            |            |
  |                              |                              |            |            |
  |<-- CheckInResponse           |                              |            |            |
  |    (ordered module list) ----|                              |            |            |
  |                              |                              |            |            |
  |-- For each module/resource:  |                              |            |            |
  |   1. Check current state     |                              |            |            |
  |   2. Snapshot if file change |                              |            |            |
  |   3. Apply if drift detected |                              |            |            |
  |   4. Fire notify triggers    |                              |            |            |
  |   5. Record result           |                              |            |            |
  |                              |                              |            |            |
  |-- ReportResult(run_id,       |                              |            |            |
  |   per-module results) ------>|                              |            |            |
  |                              |-- Store run + results ------>|            |            |
  |                              |-- Update compliance status ->|            |            |
  |                              |-- Fire webhooks if needed    |            |            |
  |                              |                              |            |            |
  |<-- ReportAck ----------------|                              |            |            |
```

#### 3. Secret Rotation Flow

```
isopass                        Server                        PostgreSQL     Agents
  |                              |                              |              |
  | (secret value changes)       |                              |              |
  |                              |                              |              |
  |                              |-- Poll watched paths ------->|              |
  |<-- Get version/hash ---------|                              |              |
  |-- Return new version ------->|                              |              |
  |                              |                              |              |
  |                              |-- Detect version change      |              |
  |                              |-- Record rotation event ---->|              |
  |                              |-- Find affected clients ---->|              |
  |                              |-- Queue force-run flags ---->|              |
  |                              |                              |              |
  |                              |  (on next check-in, or       |              |
  |                              |   force-push triggers        |              |
  |                              |   immediate convergence)     |              |
  |                              |                              |              |
  |                              |-- If TLS cert rotated:       |              |
  |                              |   hot-reload via             |              |
  |                              |   GetCertificate callback    |              |
```

#### 4. Admin Operations Flow (gvctl / Web UI)

```
gvctl / Web UI                 Server REST API               PostgreSQL
  |                              |                              |
  |-- GET/POST /api/v1/... ----->|                              |
  |                              |-- Query/mutate state ------->|
  |                              |<-- Results ------------------|
  |<-- JSON response ------------|                              |
  |                              |                              |
  | (gvctl push triggers         |                              |
  |  ForcePush flag in DB,       |                              |
  |  agent picks up on           |                              |
  |  next check-in)              |                              |
```

## Patterns to Follow

### Pattern 1: Provider Interface for External Dependencies

**What:** Wrap all external services (isopass, package managers, fact collectors) behind Go interfaces. Inject concrete implementations at startup.

**When:** Any interaction with an external system or OS-level command.

**Why:** Enables testing with mock implementations. Decouples business logic from infrastructure. Allows the `StaticProvider` for dev mode without isopass running.

**Example:**
```go
// secrets/provider.go
type Provider interface {
    Get(ctx context.Context, key string) (string, error)
    GetBytes(ctx context.Context, key string) ([]byte, error)
    Close() error
}

// Concrete implementations: IsopassProvider, StaticProvider
// Injected at main() startup based on config
```

**Confidence:** HIGH -- this is the pattern specified in the handoff document and matches standard Go practice.

### Pattern 2: Check-Before-Act Idempotency

**What:** Every module executor follows the same pattern: read current state, compare to desired state, act only if different, report action taken.

**When:** All resource types (file, package, service, user).

**Why:** This is the core contract of configuration management. Violating it causes unnecessary restarts, file churn, and audit noise.

**Example:**
```go
type Executor interface {
    // Check returns the current state and whether drift exists
    Check(ctx context.Context, desired Resource) (DriftResult, error)
    // Apply converges to desired state. Only called if Check found drift.
    Apply(ctx context.Context, desired Resource) (ActionResult, error)
}

// The runner calls Check first, only calls Apply if drift detected
```

**Confidence:** HIGH -- this is a well-established pattern in Puppet (ensure/property model), Chef (why-run mode), and the handoff spec.

### Pattern 3: DAG-Based Execution Ordering

**What:** Build a directed acyclic graph from `depends_on` declarations at both module and resource level. Topologically sort before execution. Detect cycles at config load time, not at run time.

**When:** Whenever the server compiles a catalog for a client check-in.

**Why:** Implicit ordering (list order) breaks when modules are composed from multiple sources. Explicit DAG makes dependencies visible and debuggable. Cycle detection at load time prevents runtime hangs.

**Implementation note:** Use Kahn's algorithm (BFS with in-degree tracking). It naturally produces a deterministic order for nodes at the same depth level and makes cycle detection trivial (remaining nodes after algorithm completes = cycle members). The `gammazero/toposort` Go package implements this, but the algorithm is simple enough to write in ~50 lines. Recommend implementing it in-house to avoid a dependency for such a small piece of logic.

**Confidence:** HIGH -- topological sort for resource ordering is proven across Puppet, Chef, Terraform, and the handoff spec explicitly requires it.

### Pattern 4: Filesystem as Source of Truth with Watch

**What:** YAML config files on disk are the single source of truth. The server watches the `config/` directory tree with `fsnotify` and reloads on changes. No API, DB, or UI writes to config.

**When:** All configuration changes -- client configs, module definitions, classification rules, server config.

**Why:** GitOps-friendly (commit, push, server picks up changes). Single path eliminates config drift between DB and files. Auditability via git log.

**Implementation note:** Use `fsnotify/fsnotify` for filesystem watching. Debounce rapid changes (e.g., git operations touch many files quickly). Validate YAML and DAG integrity on reload before swapping the active config. Log validation errors but keep serving the last-known-good config if the new config is invalid.

**Confidence:** HIGH -- specified in handoff. Standard GitOps pattern.

### Pattern 5: Notify/Subscribe Trigger Chain

**What:** Resources declare `notify` lists. If a resource makes a change (action_taken != none), all notified resources are forced to re-apply regardless of their own drift check. This is how "restart service when config file changes" works.

**When:** Service restarts/reloads triggered by file changes, user creation triggering subsequent config.

**Why:** Without notify/subscribe, services would not pick up config changes until the next full convergence cycle or manual restart.

**Implementation note:** Process notify triggers within the same DAG walk. After applying a resource, if it changed, mark all notified resources as "force-apply" before their Check phase. This must respect the topological order -- the notified resource must come after the notifier in the DAG.

**Confidence:** HIGH -- this is Puppet's notify/subscribe model verbatim. Proven at massive scale.

### Pattern 6: Signed Request Authentication

**What:** Every gRPC request from an agent includes a signature over the serialized protobuf body + UTC timestamp, signed with the agent's ED25519 private key. The server verifies using the stored public key for that client UUID.

**When:** Every agent-to-server RPC after registration.

**Why:** mTLS authenticates the transport layer, but ED25519 signatures authenticate the application layer. This prevents a compromised client cert from impersonating a different agent. The timestamp prevents replay attacks.

**Implementation note:** Use a gRPC interceptor (UnaryInterceptor) on the server side to verify signatures before the handler runs. Use gRPC metadata to carry the signature and timestamp. Reject requests with timestamps older than 5 minutes.

**Confidence:** HIGH -- specified in handoff. Standard defense-in-depth pattern.

## Anti-Patterns to Avoid

### Anti-Pattern 1: Mixing Config Storage Paths

**What:** Allowing configs to be created/modified via API, CLI, or UI in addition to filesystem YAML.

**Why bad:** Creates two sources of truth. Configs in the database drift from configs on disk. Impossible to know which is authoritative. Git audit trail breaks.

**Instead:** Filesystem YAML is the sole source of truth. CLI/API/UI are read-only for config. Operations (tokens, revocations, force-push) go through the API. Config changes go through file edits (and ideally git commits).

### Anti-Pattern 2: Global State or Singletons for Providers

**What:** Using package-level variables or `init()` functions to wire up the secrets provider, database connection, or config loader.

**Why bad:** Untestable. Hidden dependencies. Initialization order bugs. Impossible to run multiple instances in tests.

**Instead:** Wire everything in `main()`. Pass dependencies via constructor injection. Use interfaces for all external boundaries.

### Anti-Pattern 3: Applying Changes Without Checking First

**What:** Module executors that always write the file, always install the package, always restart the service, and then report "changed."

**Why bad:** Generates noise in audit logs. Causes unnecessary service restarts. Makes compliance tracking meaningless (every run shows "changed"). Wastes resources.

**Instead:** Every executor must implement the check phase. Only apply when the check shows drift. Record `action_taken: none` when no change was needed.

### Anti-Pattern 4: Blocking the Agent Pull Loop on Failures

**What:** Agent crashes or enters an infinite retry loop when the server is unreachable or returns an error.

**Why bad:** A server restart would take down all managed nodes' convergence loops. Network blips would require manual agent restarts.

**Instead:** Exponential backoff with jitter on server unavailability. Never crash the daemon on transient errors. Log and continue. Only hard-fail on startup if critical prerequisites are missing (keypair, isopass).

### Anti-Pattern 5: Resolving Secrets at Config Parse Time and Caching Indefinitely

**What:** Resolving all `secret:` references once at startup and never refreshing.

**Why bad:** Secret rotation becomes invisible. TLS certs expire. Database password rotations lock out the server.

**Instead:** Use `GetCertificate` callback for TLS hot-reload. Re-resolve secrets on the rotation watcher's signal. For the DB DSN, reconnect with the new credential when the watcher detects a change.

## Server Internal Architecture

The server process hosts multiple subsystems that should be structured as distinct packages with clear boundaries:

```
server/
  main.go                    # Wiring: construct all components, start listeners
  grpc/
    server.go                # gRPC server setup, mTLS config
    interceptor.go           # Signature verification interceptor
    handlers.go              # Register, CheckIn, ReportResult, ForcePush, DryRun, Rollback, ReportFacts
  api/
    router.go                # REST API routes (chi or stdlib mux)
    middleware.go            # Auth, logging, recovery
    handlers_clients.go      # /api/v1/clients/*
    handlers_runs.go         # /api/v1/runs/*
    handlers_compliance.go   # /api/v1/compliance/*
    handlers_tokens.go       # /api/v1/approved/*, /api/v1/tokens/*
    handlers_webhooks.go     # /api/v1/webhooks/*
    handlers_packages.go     # /api/v1/packages/*
    handlers_env.go          # /api/v1/env/*
    handlers_agent.go        # /api/v1/agent/*, /bootstrap
  config/
    loader.go                # YAML loading, secret: resolution, env_var merging
    watcher.go               # fsnotify-based config reload
    types.go                 # Config structs
  registry/
    registry.go              # Client enrollment, pattern matching, token validation
  runner/
    dag.go                   # DAG construction, topological sort, cycle detection
    compiler.go              # Catalog compilation: config + facts + classification -> ordered modules
    notify.go                # Notify/subscribe trigger processing
  classifier/
    classifier.go            # Auto-classification from classification.yaml
  compliance/
    tracker.go               # Compliance status computation from run results
    history.go               # Historical compliance data
  planner/
    planner.go               # Dry-run plan computation
  webhook/
    dispatcher.go            # HMAC-signed webhook delivery with retries
  watcher/
    rotation.go              # isopass secret rotation polling
  secrets/
    provider.go              # Provider interface
    isopass.go               # IsopassProvider
    static.go                # StaticProvider (dev/test)
  db/
    queries.go               # Database access (or use sqlc-generated code)
    migrations/              # golang-migrate migration files
  metrics/
    prometheus.go            # Metric registration and update
```

### Key Internal Boundaries

1. **Config Loader** produces a typed `ServerConfig` struct and per-client `ClientConfig` structs. It owns all YAML parsing and `secret:` resolution. No other package reads YAML directly.

2. **Runner/Compiler** takes a `ClientConfig` + facts and produces an ordered `[]Module` (the catalog). It owns DAG construction, topological sort, `when:` evaluation, and env_var merging. The gRPC handler simply calls `compiler.Compile(clientID, facts)` and returns the result.

3. **Registry** owns all enrollment logic. The gRPC `Register` handler delegates entirely to `registry.Register()`. Pattern matching, token validation, and pubkey storage are encapsulated here.

4. **Compliance Tracker** is updated after every `ReportResult` call. It reads run results and computes the new status. The REST API queries it for dashboard data.

5. **Webhook Dispatcher** is event-driven: other components call `dispatcher.Fire(event)` and it handles delivery asynchronously with retries. It never blocks the caller.

## Agent Internal Architecture

```
agent/
  main.go                    # Wiring, daemon vs one-shot mode
  auth/
    keypair.go               # ED25519 key generation/loading
    signer.go                # Request signing for gRPC calls
    registration.go          # Registration flow
  checkin/
    loop.go                  # Jittered pull loop with backoff
    client.go                # gRPC client wrapper
  facts/
    collector.go             # Collector interface
    os.go                    # OS facts (family, distro, version, kernel, arch)
    hardware.go              # CPU count, memory, disk
    network.go               # Hostname, FQDN, IPs, MACs
    packages.go              # Installed packages from package manager
  modules/
    executor.go              # Executor interface (Check + Apply)
    file.go                  # File executor (SHA256 check, snapshot, write)
    package.go               # Package executor (delegates to pkgmgr)
    service.go               # Service executor (systemctl)
    user.go                  # User executor (/etc/passwd)
    pkgmgr/
      interface.go           # PackageManager interface
      apt.go                 # apt-get backend
      dnf.go                 # dnf backend
      yum.go                 # yum backend
      apk.go                 # apk backend
      pacman.go              # pacman backend
      brew.go                # brew backend
      zypper.go              # zypper backend
      detect.go              # Auto-detection from OS facts
  snapshots/
    manager.go               # File snapshot creation, retention (last 5), rollback restoration
  config/
    agent.go                 # Agent config loading (/etc/isogov/agent.yaml)
```

### Agent Execution Flow (Per Check-In)

```
1. Collect all facts via Collector implementations
2. Sign CheckIn request with ED25519 key
3. Send CheckIn(UUID, facts, state_fingerprint) to server
4. Receive ordered module list
5. For each module (in server-provided order):
   a. For each resource (in DAG order within module):
      i.   Evaluate when: condition against facts -> skip if false
      ii.  Check current state (idempotency guard)
      iii. If no drift -> record action_taken: none, continue
      iv.  If file type -> snapshot current file
      v.   Apply change
      vi.  If change succeeded and resource has notify list ->
           mark notified resources as force-apply
      vii. Record result (action, drift, error)
6. Sign RunReport with ED25519 key
7. Send ReportResult(run_id, per-module results) to server
8. Check for self-update (version check against server)
9. Calculate next check-in time with jitter
```

## Scalability Considerations

| Concern | At 10 nodes | At 1,000 nodes | At 10,000+ nodes |
|---------|-------------|----------------|-------------------|
| **Check-in load** | Negligible | ~33 check-ins/min at 30min interval; single server handles easily | ~333 check-ins/min; may need connection pooling, consider read replicas for queries |
| **Config compilation** | Trivial | Cache compiled catalogs, invalidate on config file change | Pre-compile catalogs on config change, serve from cache |
| **Fact storage** | Store all | JSONB with last-10 retention policy keeps it bounded | Consider partitioning client_facts by client_id or time |
| **Database connections** | Default pool (10-25) | Tune pool size, connection lifetime | PgBouncer for connection pooling; read replicas for REST API reads |
| **Webhook delivery** | Inline is fine | Async with bounded worker pool (already required) | Dedicated webhook worker, message queue if delivery latency matters |
| **gRPC concurrency** | Default Go gRPC settings | Tune `MaxConcurrentStreams`, keep-alive settings | Multiple server instances behind load balancer, sticky sessions by client UUID |

**Note:** The handoff spec describes a single-server architecture. At the 10,000+ node scale, a multi-server setup with shared PostgreSQL would be needed. This is out of scope for the initial build but the architecture (stateless server, shared DB) naturally supports horizontal scaling.

## Suggested Build Order

The build order is driven by dependency chains. Each layer depends on the layers above it.

```
Layer 0 (Foundation - no dependencies):
  proto/isogov.proto           # Define all messages/services first
  secrets/provider.go          # Interface + StaticProvider for dev
  db/migrations/               # Schema locked in early

Layer 1 (Server skeleton - depends on Layer 0):
  server/main.go               # gRPC + REST listeners, TLS, DB pool
  server/db/                   # Database access layer

Layer 2 (Core server logic - depends on Layer 1):
  server/registry/             # Enrollment, pattern matching, tokens
  server/config/loader.go      # YAML parsing, secret: resolution

Layer 3 (Agent skeleton - depends on Layer 0 + Layer 2 running):
  agent/auth/                  # Keypair, registration flow
  agent/config/                # Agent config loading
  agent/facts/                 # All fact collectors

Layer 4 (Catalog compilation - depends on Layer 2 + Layer 3):
  server/classifier/           # Auto-classification
  server/runner/               # DAG, compiler, notify
  server/config/watcher.go     # fsnotify reload

Layer 5 (Agent execution - depends on Layer 3 + Layer 4):
  agent/modules/               # All executors (file, package, service, user)
  agent/modules/pkgmgr/        # Package manager backends
  agent/snapshots/             # File snapshot manager
  agent/checkin/               # Pull loop, reporting

Layer 6 (Operations - depends on Layers 1-5 working):
  server/compliance/           # Status tracking
  server/planner/              # Dry-run
  server/api/                  # REST API handlers
  cli/ (gvctl)                 # CLI tool

Layer 7 (Observability & Integrations - depends on Layer 6):
  server/metrics/              # Prometheus
  server/webhook/              # Webhook dispatcher
  server/watcher/              # Secret rotation watcher
  secrets/isopass.go           # IsopassProvider (real integration)

Layer 8 (User Interface - depends on Layer 6):
  ui/                          # Web UI SPA
  server/api/handlers_agent.go # Bootstrap script, self-update endpoints

Layer 9 (Packaging - depends on everything):
  Dockerfile.server
  Dockerfile.agent
  docker-compose.yml
  Makefile
```

### Build Order Rationale

1. **Proto first** because every component depends on the message definitions. Changing proto later causes cascading regeneration.

2. **Secrets provider interface early** because the config loader, server startup, and eventually every component that touches credentials depends on it. The `StaticProvider` unblocks development without requiring isopass to be running.

3. **Database migrations early** because the schema constrains the data model. Getting the schema right early prevents migration headaches later.

4. **Registration before check-in** because you cannot test the pull loop without enrolled clients.

5. **Facts before catalog compilation** because the compiler needs facts for `when:` evaluation and env_var merging. Facts also feed auto-classification.

6. **Module executors are Layer 5** (not earlier) because they need the full pipeline working: server delivers ordered modules, agent executes them. Testing executors in isolation is possible but integration testing requires Layers 0-4.

7. **Compliance, dry-run, and REST API are Layer 6** because they are consumer-facing features that depend on the core loop working correctly first.

8. **isopass real integration is Layer 7** (not Layer 0) because the `StaticProvider` allows full development without isopass. The real integration requires reading the isopass source code, understanding its API, and may involve debugging. Deferring it avoids blocking early development. Wire it in once the core system is proven.

9. **Web UI last** because it is purely a consumer of the REST API. It adds no new server-side logic. Building it last ensures the API is stable before the UI depends on it.

## Technology Decisions Informing Architecture

| Decision | Impact on Architecture |
|----------|----------------------|
| **gRPC for agent communication** | Binary protocol, code-generated clients/servers, natural for streaming if needed later. Server needs both gRPC (`:9090`) and HTTP (`:8080`) listeners. |
| **REST for human-facing tools** | CLI and Web UI use standard HTTP. No proto dependency in the CLI. Server multiplexes both protocols. |
| **PostgreSQL as sole persistent store** | All state (clients, runs, facts, plans, compliance) in one database. Simplifies ops. JSONB for semi-structured data (facts). |
| **Filesystem YAML as config source** | Server must implement a file watcher. Config is never in the database. Server is stateless with respect to desired-state config (can be reconstructed from files). |
| **`go:embed` for Web UI** | Single server binary includes the SPA. No separate static file server. Simplifies deployment. |
| **ED25519 + mTLS (defense in depth)** | Two auth layers. mTLS at transport, ED25519 at application. gRPC interceptor handles signature verification. |

## Sources

- [Puppet Architecture Overview](https://www.puppet.com/docs/puppet/5.5/architecture.html) -- pull-based catalog compilation model (HIGH confidence)
- [The Architecture of Open Source Applications: Puppet](https://aosabook.org/en/v2/puppet.html) -- comprehensive internal architecture (HIGH confidence)
- [Puppet Internals: the compiler](https://www.masterzen.fr/2012/03/17/puppet-internals-the-compiler/) -- catalog compilation details (MEDIUM confidence)
- [Dependency graph resolution in Go](https://dnaeon.github.io/dependency-graph-resolution-algorithm-in-go/) -- DAG implementation patterns (MEDIUM confidence)
- [gammazero/toposort Go package](https://pkg.go.dev/github.com/gammazero/toposort) -- Kahn's algorithm implementation (HIGH confidence)
- [Go Project Structure: Clean Architecture Patterns](https://dasroot.net/posts/2026/01/go-project-structure-clean-architecture/) -- Go project layout patterns (MEDIUM confidence)
- [Building Production Grade Microservices with Go and gRPC](https://dev.to/nikl/building-production-grade-microservices-with-go-and-grpc-a-step-by-step-developer-guide-with-example-2839) -- gRPC service patterns (MEDIUM confidence)
- Isogov handoff document (`isogov-handoff.md`) -- authoritative project specification (HIGH confidence)
