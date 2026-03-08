# Hobnob — Claude Code Handoff Document

## Project Overview

Build **Hobnob**, a Puppet-style configuration management system written in Go with a PostgreSQL backend. All components are containerized. All traffic is TLS-protected. Clients authenticate via SSH public key exchange after an approved-list + one-time-token bootstrap.

---

## Technology Stack

| Layer | Technology |
|---|---|
| Language | Go (latest stable) |
| Transport | gRPC over mTLS |
| Database | PostgreSQL 16 |
| Containerization | Docker + Docker Compose |
| TLS | Self-signed CA (dev) / bring-your-own cert (prod) |
| Client auth | ED25519 SSH keypair |
| Config format | YAML |
| Metrics | Prometheus |
| Secrets backend | isopass (see section below) |

---

## Repository Structure

```
hobnob/
├── proto/
│   └── hobnob.proto          # gRPC service definitions
├── server/
│   ├── main.go
│   ├── grpc/                    # gRPC handlers
│   ├── api/                     # REST API for UI + CLI
│   ├── db/                      # PostgreSQL queries (sqlc or raw)
│   ├── config/                  # Server config loader
│   ├── registry/                # Client registration logic
│   ├── runner/                  # Module dispatch + idempotency engine
│   └── webhook/                 # Webhook dispatcher
├── agent/
│   ├── main.go
│   ├── checkin/                 # Pull loop with jitter
│   ├── modules/                 # Module executors
│   │   ├── file.go
│   │   ├── package.go
│   │   ├── service.go
│   │   └── user.go
│   └── auth/                    # Keypair management + registration
├── cli/                         # hobcli admin CLI
│   └── main.go
├── ui/                          # Lightweight SPA (served by server)
├── config/
│   ├── server.yaml              # Global server config
│   └── clients/                 # Per-client YAML configs
│       ├── web01.yaml
│       └── db-*.yaml
├── modules/                     # Reusable module definitions
│   ├── nginx.yaml
│   └── base-users.yaml
├── migrations/                  # PostgreSQL migrations (golang-migrate)
├── docker-compose.yml
├── Dockerfile.server
├── Dockerfile.agent
└── Makefile
```

---

## gRPC Service Definition (`proto/hobnob.proto`)

Define the following services and messages:

```protobuf
service Hobnob {
  rpc Register(RegisterRequest) returns (RegisterResponse);
  rpc CheckIn(CheckInRequest) returns (CheckInResponse);
  rpc ReportResult(RunReport) returns (ReportAck);
  rpc ForcePush(ForcePushRequest) returns (ForcePushResponse); // called by hobcli
}
```

**Register flow:**
- Client sends: hostname, IP address, ED25519 public key (PEM), one-time token
- Server validates: hostname/IP matches an approved pattern AND token is valid and unused
- Server responds: success/failure, assigned client UUID

**CheckIn flow:**
- Client sends: client UUID, current state fingerprint
- Server responds: list of modules to apply, with full desired state for each

**ReportResult flow:**
- Client sends: run UUID, per-module results (action taken, drift detected, errors)
- Server stores results, triggers webhooks if configured

All requests must be signed with the client's registered ED25519 private key. The server verifies the signature using the stored public key for that client UUID. Include a timestamp in every signed payload to prevent replay attacks (reject requests older than 5 minutes).

---

## Authentication & Registration Flow

### Bootstrap

1. Admin runs: `hobcli approved add "*.web.example.com" --token <one-time-token>`
2. Server stores the pattern + hashed token in `approved_patterns` table
3. Admin installs agent on client node with the token baked in as an env var or config file

### First Run (agent side)

1. Agent generates ED25519 keypair, stores private key locally (e.g. `/etc/hobnob/agent.key`)
2. Agent calls `Register()` gRPC with hostname, IP, pubkey, token
3. Server checks:
   - Does hostname OR IP match any approved pattern? (wildcard glob matching)
   - Is the token valid and unused?
4. If yes: server stores pubkey, marks client enrolled, marks token used, returns client UUID
5. Agent stores UUID locally. Token is discarded.

### Subsequent Check-ins

- All gRPC calls include a signed payload (sign the serialized protobuf body + UTC timestamp)
- Server looks up pubkey by client UUID and verifies signature
- No token needed again

### Wildcard Pattern Matching

Patterns match against EITHER the hostname (FQDN) OR the IP address string. Use standard glob syntax (`*`, `?`). Examples:
- `*.web.example.com` matches `web01.web.example.com`
- `10.0.1.*` matches `10.0.1.55`
- `db-*` matches `db-primary`

---

## Database Schema

Use `golang-migrate` for migrations. All tables use UUIDs as primary keys.

```sql
-- Approved patterns for client registration
CREATE TABLE approved_patterns (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  pattern TEXT NOT NULL,
  token_hash TEXT NOT NULL,        -- bcrypt hash of the one-time token
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  used_at TIMESTAMPTZ,
  used_by UUID                     -- FK to clients.id once used
);

-- Enrolled clients
CREATE TABLE clients (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  hostname TEXT NOT NULL,
  ip_address TEXT NOT NULL,
  pubkey_pem TEXT NOT NULL,        -- ED25519 public key in PEM format
  enrolled_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen TIMESTAMPTZ,
  revoked BOOLEAN NOT NULL DEFAULT false,
  revoked_at TIMESTAMPTZ
);

-- Individual runs (one per check-in)
CREATE TABLE runs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  client_id UUID NOT NULL REFERENCES clients(id),
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at TIMESTAMPTZ,
  status TEXT NOT NULL CHECK (status IN ('running', 'success', 'failure', 'partial'))
);

-- Per-module results within a run
CREATE TABLE run_results (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id UUID NOT NULL REFERENCES runs(id),
  module_name TEXT NOT NULL,
  resource_type TEXT NOT NULL,     -- file, package, service, user
  resource_name TEXT NOT NULL,     -- e.g. /etc/nginx/nginx.conf
  action_taken TEXT NOT NULL,      -- none, created, updated, deleted
  drift_detected BOOLEAN NOT NULL DEFAULT false,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Webhook configurations
CREATE TABLE webhooks (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  url TEXT NOT NULL,
  events TEXT[] NOT NULL,          -- e.g. ['run.failure', 'drift.detected']
  secret TEXT NOT NULL,            -- HMAC-SHA256 signing secret
  enabled BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

---

## Server Configuration (`config/server.yaml`)

```yaml
server:
  grpc_listen: ":9090"
  api_listen: ":8080"
  tls:
    cert_file: "/certs/server.crt"
    key_file: "/certs/server.key"
    ca_file: "/certs/ca.crt"       # For verifying client certs (mTLS)

database:
  dsn: "postgres://hobnob:secret@postgres:5432/hobnob?sslmode=disable"

checkin:
  base_interval: "30m"             # Clients should use this as their base interval
  jitter_percent: 20               # ± 20%

webhooks:
  timeout: "10s"
  max_retries: 3
```

---

## Per-Client Config (`config/clients/web01.yaml`)

```yaml
client: web01.example.com          # Must match enrolled hostname

modules:
  - name: nginx-config
    source: modules/nginx.yaml
    vars:
      worker_processes: 4
      server_name: web01.example.com

  - name: base-users
    source: modules/base-users.yaml

  - name: deploy-app
    type: file
    resources:
      - path: /etc/myapp/config.ini
        content: |
          [server]
          port = 8080
        owner: myapp
        group: myapp
        mode: "0640"
```

---

## Module Format (`modules/nginx.yaml`)

All modules declare a `type` per resource. All resources must be idempotent.

```yaml
name: nginx-config
description: "Install and configure nginx"

resources:
  - type: package
    name: nginx
    state: present                 # present | absent

  - type: file
    path: /etc/nginx/nginx.conf
    source: templates/nginx.conf.tmpl   # Go template, vars injected
    owner: root
    group: root
    mode: "0644"

  - type: service
    name: nginx
    enabled: true
    state: running                 # running | stopped

  - type: user
    name: www-data
    uid: 33
    shell: /usr/sbin/nologin
    home: /var/www
    state: present
```

---

## Module Idempotency Rules

Each module executor must check current state before taking action and only act if a change is required.

| Resource Type | How to Check Current State | Idempotency Guard |
|---|---|---|
| `file` | SHA256 hash of existing file | Skip write if hash matches desired |
| `package` | Query package manager for installed version | Skip if already at desired version/state |
| `service` | `systemctl is-active` + `systemctl is-enabled` | Skip if state already matches |
| `user` | Parse `/etc/passwd` for user existence + attrs | Skip if all attributes already match |

Always record `action_taken: none` in run results when no change was needed.

---

## Agent Behavior

### Daemon Loop

```
1. On start: verify keypair exists and client is enrolled. If not, run registration.
2. Calculate next check-in: base_interval ± (base_interval * jitter_percent/100 * random[-1,1])
3. Sleep until next check-in
4. Call CheckIn() → receive module list
5. Execute each module in order, recording results
6. Call ReportResult() with full run report
7. Go to step 2
```

### Agent Config (`/etc/hobnob/agent.yaml`)

```yaml
server: "hobnob.example.com:9090"
tls:
  ca_file: "/etc/hobnob/ca.crt"
key_file: "/etc/hobnob/agent.key"
client_id_file: "/etc/hobnob/client.uuid"
registration_token: ""             # Only needed on first run; cleared after enrollment
log_level: "info"
```

---

## CLI Tool (`hobcli`)

Implement the following subcommands. `hobcli` talks to the server REST API (not gRPC directly).

```
hobcli approved add <pattern> --token <token>   # Add to approved list
hobcli approved list                            # List all approved patterns
hobcli approved revoke <pattern>                # Remove pattern

hobcli clients list                             # List all enrolled clients
hobcli clients show <hostname-or-uuid>          # Full detail + last run
hobcli clients revoke <hostname-or-uuid>        # Revoke client (blocks future check-ins)

hobcli push <hostname-or-uuid>                  # Force immediate check-in via ForcePush RPC
hobcli push --all                               # Force all clients

hobcli runs list [--client <name>] [--limit 20] # Recent runs
hobcli runs show <run-uuid>                     # Full run detail with per-resource results

hobcli tokens generate                          # Generate a cryptographically random one-time token
hobcli webhooks add <url> --events run.failure,drift.detected
hobcli webhooks list
hobcli webhooks delete <id>
```

---

## Web UI

Serve as a compiled SPA from the server binary (embed with `go:embed`). Implement these views:

- **Dashboard**: total clients, clients seen in last hour, recent failures, drift rate
- **Clients**: table of all clients — hostname, IP, enrolled date, last seen, status badge
- **Client detail**: last N runs, expandable per-module results, drift history chart
- **Runs**: filterable run log across all clients
- **Config**: view-only display of the YAML config for each client (read from disk)

Tech choice for the SPA is up to you — keep it simple (vanilla JS + htmx is fine, or a minimal React build).

---

## Prometheus Metrics

Expose on `GET /metrics`. Include at minimum:

```
hobnob_clients_total                        # Gauge: total enrolled clients
hobnob_clients_active_total                 # Gauge: clients seen in last 1h
hobnob_checkins_total{status}               # Counter: check-ins by status
hobnob_run_duration_seconds{client}         # Histogram: run duration
hobnob_drift_detected_total{client,module}  # Counter: drift events
hobnob_module_actions_total{type,action}    # Counter: actions taken by type
```

---

## Webhook Payload

POST to configured URL, signed with `X-Hobnob-Signature: sha256=<hmac>`.

```json
{
  "event": "run.failure",
  "timestamp": "2025-01-01T00:00:00Z",
  "client": {
    "id": "uuid",
    "hostname": "web01.example.com"
  },
  "run": {
    "id": "uuid",
    "status": "failure",
    "modules_run": 4,
    "modules_failed": 1
  }
}
```

Events to support: `run.success`, `run.failure`, `run.partial`, `drift.detected`, `client.enrolled`, `client.revoked`

---

## Docker Compose

```yaml
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: hobnob
      POSTGRES_USER: hobnob
      POSTGRES_PASSWORD: secret
    volumes:
      - pgdata:/var/lib/postgresql/data

  server:
    build:
      dockerfile: Dockerfile.server
    ports:
      - "9090:9090"   # gRPC
      - "8080:8080"   # REST API + UI + metrics
    volumes:
      - ./config:/config:ro
      - ./certs:/certs:ro
    depends_on:
      - postgres

  # Agent is NOT run in compose — it is installed on each managed node separately

volumes:
  pgdata:
```

---

## TLS Setup

Include a `make certs` target in the Makefile that uses `openssl` to:

1. Generate a CA keypair (`ca.key`, `ca.crt`)
2. Generate a server keypair signed by the CA (`server.key`, `server.crt`) with SANs for `localhost` and `hobnob`
3. Output all certs to `./certs/`

For production, document how to swap in a cert from Let's Encrypt or an internal CA by replacing the files in the certs volume.

---

## Secrets Integration — isopass

**Before writing any secrets-related code, Claude Code must:**

1. Read and understand the isopass source at `~/repos/isopass`
2. Identify the public Go API surface — the client constructor, fetch/get methods, auth mechanism, and any error types
3. Note whether isopass uses a daemon/socket, HTTP API, Unix socket, or is a pure library
4. Note how isopass authenticates callers (token, mTLS, OS user, etc.)
5. Design the Hobnob integration based on what you find — do not assume an API shape

**Integration requirements (implement once you understand the API):**

Hobnob must use isopass as the **sole source of secrets at runtime**. Secrets must never be stored in YAML config files, environment variables, or the database in plaintext. This includes:

- The PostgreSQL DSN / password
- TLS private keys and certificates (server cert, CA cert)
- Webhook HMAC signing secrets
- Any future credentials added to the system

### Secrets Provider Abstraction

Implement a `secrets` package that wraps isopass behind an interface so the rest of the codebase is not coupled to isopass directly:

```go
// server/secrets/provider.go
package secrets

type Provider interface {
    // Get retrieves a secret by its path/key as understood by isopass
    Get(ctx context.Context, key string) (string, error)

    // GetBytes retrieves a binary secret (e.g. a TLS cert/key PEM block)
    GetBytes(ctx context.Context, key string) ([]byte, error)

    // Close cleans up any connections or state
    Close() error
}
```

Implement `IsopassProvider` that satisfies this interface using the real isopass client. Also implement a `StaticProvider` that reads from environment variables — used only for local dev/testing when isopass is not available.

### Where Secrets Are Referenced in Config

In `config/server.yaml` and per-client YAML files, secret values are replaced with isopass path references using a `secret:` prefix so it is unambiguous:

```yaml
database:
  dsn: "secret:hobnob/db/dsn"          # isopass path, not a literal value

server:
  tls:
    cert_file: "secret:hobnob/tls/cert"
    key_file: "secret:hobnob/tls/key"
    ca_file: "secret:hobnob/tls/ca"

webhooks:
  - url: "https://hooks.example.com/hobnob"
    secret: "secret:hobnob/webhooks/hook1-hmac"
```

The config loader must detect the `secret:` prefix and resolve the value through the `Provider` interface at startup before the resolved value is used anywhere.

### Module-Level Secrets

Module YAML files can also reference secrets for resources that require them (e.g. a file resource containing a password, a user resource with an initial password hash):

```yaml
resources:
  - type: file
    path: /etc/myapp/db.conf
    content: "secret:myapp/db/config-file-content"
    owner: myapp
    mode: "0600"
```

The agent's module executor must resolve `secret:` values through the provider before applying the resource.

### isopass Client Lifecycle

- Instantiate the isopass client once at server/agent startup
- Pass it through the application via the `Provider` interface (do not use globals)
- If isopass is unreachable at startup, fail fast with a clear error — do not start with missing secrets
- Log secret access at DEBUG level (key path only, never the value)
- Never log, print, or include secret values in error messages, run results, or the database

### isopass in Docker Compose

Once you have read the isopass repo and understand how it runs (standalone binary, sidecar, socket, etc.), add it to `docker-compose.yml` appropriately. If it requires a config file or volume mount, include that. The server container must be able to reach isopass at startup.

---

## Implementation Order (Suggested)

1. **Read `~/repos/isopass` in full** — understand the API before writing anything else
2. `proto/hobnob.proto` — define all messages and services first
3. `server/secrets/` — implement `Provider` interface + `IsopassProvider` + `StaticProvider` (dev/test fallback)
4. Database migrations — get the schema locked in
5. Server skeleton — gRPC server, TLS loaded via secrets provider, DB connection pool
6. Registration logic — approved pattern matching, token validation, pubkey storage
7. Agent skeleton — keypair generation, registration flow, secrets provider wired in
8. CheckIn + module dispatch (server side) — YAML config loading with `secret:` resolution
9. Agent module executors — file, package, service, user (all idempotent; secrets resolved before apply)
10. Agent run loop — jittered daemon, reporting
11. REST API + `hobcli` CLI
12. Prometheus metrics
13. Webhook dispatcher
14. Web UI
15. Docker Compose + Makefile + certs automation (isopass service included in compose)

---

## Key Constraints & Non-Negotiables

- All gRPC traffic uses mTLS — no plaintext connections ever
- Every client request is signature-verified against the stored pubkey
- One-time tokens are bcrypt-hashed at rest; never stored in plaintext
- All module executors MUST be idempotent — check before act, always
- Revoked clients receive an error on check-in and cannot re-enroll with the same UUID
- The agent must handle server unavailability gracefully (exponential backoff, never crash)
- `hobcli push` should queue a force-run flag in the DB that the next check-in picks up (don't require the server to initiate outbound connections to clients)
- **Secrets must never appear in plaintext** in config files, environment variables, logs, database rows, gRPC payloads, or run results — isopass is the only secrets store
- **The `secret:` prefix convention is mandatory** for any config value that is a credential — the config loader must enforce this and refuse to start if a known-secret field lacks the prefix
- **isopass unreachable = hard startup failure** — do not fall back to insecure defaults silently

---

## Module Dependency DAG

Modules within a client's config can declare explicit `depends_on` relationships. The server-side runner builds a directed acyclic graph (DAG) from these declarations and dispatches modules in topological order. Circular dependencies must be detected at config load time and rejected with a clear error.

```yaml
# config/clients/web01.yaml
modules:
  - name: base-users
    source: modules/base-users.yaml

  - name: nginx-install
    source: modules/nginx.yaml
    depends_on: [base-users]         # runs after base-users completes

  - name: nginx-config
    source: modules/nginx-config.yaml
    depends_on: [nginx-install]      # runs after nginx-install completes

  - name: nginx-service
    source: modules/nginx-service.yaml
    depends_on: [nginx-config]       # starts service only after config is written
```

Within a single module, individual resources also support `depends_on` by resource name:

```yaml
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present

  - name: write-config
    type: file
    path: /etc/nginx/nginx.conf
    depends_on: [install-nginx]

  - name: start-nginx
    type: service
    name: nginx
    state: running
    depends_on: [write-config]
```

### Notify / Subscribe (Run Triggers)

Resources can declare a `notify` list. If a resource makes a change (i.e. `action_taken != none`), all resources in its `notify` list are triggered to re-apply, even if they would otherwise be a no-op. This is the primary mechanism for "restart service when config changes."

```yaml
resources:
  - name: write-config
    type: file
    path: /etc/nginx/nginx.conf
    notify: [reload-nginx]           # triggers reload-nginx if this file changes

  - name: reload-nginx
    type: service
    name: nginx
    state: reloaded                  # state: reloaded = systemctl reload (not restart)
    subscribes: [write-config]       # alternative syntax: subscribe to a resource
```

Supported service states: `running`, `stopped`, `restarted`, `reloaded`.

---

## Node Facts & Inventory

On every check-in, the agent collects a standard set of system facts and sends them to the server alongside the run report. The server stores facts in the database and makes them available as template variables in module YAML files.

### Facts Collected by the Agent

```
os.family          # linux, darwin, windows
os.distro          # ubuntu, debian, rhel, alpine, arch, ...
os.version         # e.g. "22.04"
os.kernel          # uname -r output
os.arch            # amd64, arm64, ...
hardware.cpu_count
hardware.memory_mb
hardware.disk_gb   # map of mount point → total GB
network.hostname
network.fqdn
network.ip_addresses  # list of all non-loopback IPs
network.mac_addresses
packages.installed    # map of package name → version (from package manager)
```

Facts are collected by a pluggable `facts` package in the agent. Add new collectors by implementing:

```go
type Collector interface {
    Name() string
    Collect(ctx context.Context) (map[string]any, error)
}
```

### Using Facts in Module Templates

Facts are injected into the Go template context under a `.Facts` namespace:

```yaml
resources:
  - type: file
    path: /etc/motd
    content: |
      Welcome to {{ .Facts.network.fqdn }}
      OS: {{ .Facts.os.distro }} {{ .Facts.os.version }}
      CPUs: {{ .Facts.hardware.cpu_count }}
```

### Conditional Resources

Resources support a `when` field that is a Go template expression evaluated against facts. Resources where `when` evaluates to `false` are skipped entirely and recorded as `action_taken: skipped`.

```yaml
resources:
  - type: package
    name: apt-transport-https
    state: present
    when: '{{ eq .Facts.os.family "linux" }}'

  - type: package
    name: ufw
    state: present
    when: '{{ eq .Facts.os.distro "ubuntu" }}'
```

### Database Additions for Facts

```sql
CREATE TABLE client_facts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  client_id UUID NOT NULL REFERENCES clients(id),
  collected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  facts JSONB NOT NULL
);

-- Keep only the last 10 fact snapshots per client
-- Enforce via trigger or application logic
```

---

## Environment Tiers

Clients belong to exactly one environment: `production`, `staging`, `dev`, or any custom string. Environments allow the same module to have different variable values per tier without duplicating module files.

### Client Config with Environment

```yaml
client: web01.example.com
environment: production              # determines which env vars apply

modules:
  - name: nginx-config
    source: modules/nginx.yaml
    vars:
      worker_processes: 4            # base vars
```

### Module with Per-Environment Overrides

```yaml
name: nginx-config

vars:
  worker_processes: 2               # default
  keepalive_timeout: 65

env_vars:
  staging:
    worker_processes: 1
  production:
    worker_processes: 8
    keepalive_timeout: 120

resources:
  - type: file
    path: /etc/nginx/nginx.conf
    content: |
      worker_processes {{ .Vars.worker_processes }};
```

At render time, base `vars` are merged with the matching `env_vars` block (env-specific values win). The merged set is available as `.Vars` in templates.

### Environment Promotion via hobcli

```
hobcli env promote <module> --from staging --to production
```

This copies the current `staging` env_var overrides into `production`, creating a git-style promotion audit trail in the database.

### Database Addition for Environments

Add `environment TEXT NOT NULL DEFAULT 'production'` to the `clients` table. Add an `env_promotions` table to track promotion history.

---

## Dry Run / Change Preview

Both the agent and the server support a dry-run mode. In dry-run mode, the agent computes what it *would* do — checking current state against desired state — but does not apply any changes. Results are reported back to the server as a "plan" and stored separately from real runs.

### Agent Dry-Run Flag

```
hobnob-agent --dry-run            # one-shot dry run, exits after reporting
```

### hobcli Dry-Run (Force Push with Preview)

```
hobcli preview <hostname-or-uuid>     # triggers dry-run on a specific client
hobcli preview --all                  # dry-run across all clients
hobcli preview --env staging          # dry-run all staging clients
```

### gRPC Addition

```protobuf
rpc DryRun(DryRunRequest) returns (DryRunResponse);
```

`DryRunResponse` contains a full plan: for each resource, what action *would* be taken and why.

### Database Addition

```sql
CREATE TABLE plans (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  client_id UUID NOT NULL REFERENCES clients(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  triggered_by TEXT NOT NULL        -- 'schedule', 'hobcli', 'api'
);

CREATE TABLE plan_results (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  plan_id UUID NOT NULL REFERENCES plans(id),
  module_name TEXT NOT NULL,
  resource_type TEXT NOT NULL,
  resource_name TEXT NOT NULL,
  would_action TEXT NOT NULL,       -- none, create, update, delete, skip
  reason TEXT,                      -- human-readable explanation
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### Web UI Addition

Add a **Plans** view showing pending/recent dry-run plans. Each plan is expandable to show per-resource intended actions, color-coded by severity (create=green, update=yellow, delete=red).

---

## Rollback & File Snapshots

Before the `file` module executor overwrites or deletes a managed file, it saves a snapshot of the previous content. Snapshots are stored on the client node in `/var/lib/hobnob/snapshots/` and metadata is reported to the server.

### Snapshot Behavior

- Keep the last **5** snapshots per managed file path (configurable globally)
- Snapshot filename format: `<sha256-of-path>.<run-uuid>.bak`
- Snapshots are never deleted if a run failed mid-way — last-known-good is always recoverable
- Snapshots do NOT include files managed by `package`, `service`, or `user` resource types

### Rollback Command

```
hobcli rollback <hostname-or-uuid> --run <run-uuid>
  # Triggers the agent to restore all file snapshots taken during that run

hobcli rollback <hostname-or-uuid> --file /etc/nginx/nginx.conf
  # Triggers the agent to restore the most recent snapshot of a specific file
```

Rollback is implemented as a special `ForcePush` payload that includes rollback instructions. The agent applies them and reports results as a `rollback` run type.

### Database Addition

```sql
CREATE TABLE file_snapshots (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  client_id UUID NOT NULL REFERENCES clients(id),
  run_id UUID NOT NULL REFERENCES runs(id),
  file_path TEXT NOT NULL,
  snapshot_path TEXT NOT NULL,      -- path on the client node
  sha256_before TEXT NOT NULL,      -- hash of file before change
  sha256_after TEXT,                -- hash after change (null if deleted)
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

---

## Automatic Node Classification

Clients can be automatically assigned modules based on hostname/IP pattern matching, without requiring a hand-written `clients/<name>.yaml` for every node. Classification rules are defined in a separate `config/classification.yaml` file and are evaluated in order — first match wins.

```yaml
# config/classification.yaml
rules:
  - match: "web-*"
    environment: production
    modules:
      - source: modules/base.yaml
      - source: modules/nginx.yaml
      - source: modules/app-deploy.yaml

  - match: "db-*"
    environment: production
    modules:
      - source: modules/base.yaml
      - source: modules/postgres.yaml

  - match: "*.staging.example.com"
    environment: staging
    modules:
      - source: modules/base.yaml
      - source: modules/nginx.yaml
```

If a client has an explicit `clients/<hostname>.yaml` file, that takes full precedence over classification rules. Classification rules are the fallback. The `hobcli clients show` command displays whether a client is explicitly configured or auto-classified and which rule matched.

---

## Secret Rotation Awareness

When isopass rotates a secret that Hobnob depends on, affected nodes must re-converge promptly rather than waiting for their next scheduled check-in. Implement this via two mechanisms:

### Server-Side Secret Watcher

The server polls isopass for version changes on all watched secret paths at a configurable interval (default: 5 minutes). When a new version is detected for a secret that is referenced by one or more client configs, the server queues an immediate force-run for all affected clients.

```yaml
# config/server.yaml
secrets:
  rotation_poll_interval: "5m"
  watched_paths:
    - "hobnob/tls/cert"
    - "hobnob/tls/key"
    - "hobnob/db/dsn"
```

### Webhook from isopass (if supported)

After reading the isopass repo: if isopass supports outbound webhooks on secret rotation, implement a `POST /api/v1/secret-rotated` endpoint on the Hobnob server that accepts rotation events and immediately queues affected clients for re-convergence. Document the isopass webhook configuration needed.

### Server TLS Hot Reload

When the server's own TLS cert/key are rotated, the server must reload them without restarting. Use `crypto/tls`'s `GetCertificate` callback to fetch the current cert from isopass on each new TLS handshake, rather than loading once at startup.

### Database Addition

```sql
CREATE TABLE secret_rotation_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  secret_path TEXT NOT NULL,
  detected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  clients_queued INT NOT NULL DEFAULT 0
);
```

---

## Package Version Pinning & Auditability

The `package` resource type supports optional version pinning. The server tracks what version of each package is installed on each node and alerts on drift from pinned versions.

### Module Syntax

```yaml
resources:
  - type: package
    name: nginx
    state: present
    version: "1.24.0"              # optional pin; if omitted, latest is used
    allow_newer: false             # if true, >= version is acceptable
```

### Version Drift Detection

On every check-in, the agent reports all managed package versions as part of facts (`packages.installed`). The server compares reported versions against pinned versions in the module config and records a `version_drift` event if they diverge (e.g. a sysadmin manually upgraded a package outside Hobnob).

### Database Addition

```sql
CREATE TABLE package_audit (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  client_id UUID NOT NULL REFERENCES clients(id),
  run_id UUID NOT NULL REFERENCES runs(id),
  package_name TEXT NOT NULL,
  version_desired TEXT,
  version_installed TEXT NOT NULL,
  version_drift BOOLEAN NOT NULL DEFAULT false,
  recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### hobcli Commands

```
hobcli packages list <hostname-or-uuid>     # all managed packages + installed versions
hobcli packages drift                        # all nodes with version drift
hobcli packages drift --package nginx        # drift for a specific package across all nodes
```

---

## Multi-Platform Package Manager Support

The `package` module executor must auto-detect or allow explicit configuration of the package manager. It must never hardcode `apt` or `yum`.

### Detection Order

1. Check `package_manager:` field in the module resource (explicit override)
2. Auto-detect from facts: `os.distro` → known package manager mapping
3. Fallback: probe for binaries in order: `apt-get`, `dnf`, `yum`, `apk`, `pacman`, `brew`, `zypper`

### Supported Package Managers

| Package Manager | Distros |
|---|---|
| `apt` | Debian, Ubuntu, Mint, Kali |
| `dnf` | Fedora, RHEL 8+, CentOS Stream, Rocky, Alma |
| `yum` | RHEL 7, CentOS 7 |
| `apk` | Alpine |
| `pacman` | Arch, Manjaro |
| `brew` | macOS (Homebrew) |
| `zypper` | openSUSE, SLES |

### Package Manager Interface

Implement each as a backend satisfying:

```go
type PackageManager interface {
    IsInstalled(ctx context.Context, name string) (bool, string, error) // bool=present, string=version
    Install(ctx context.Context, name string, version string) error
    Remove(ctx context.Context, name string) error
    Upgrade(ctx context.Context, name string, version string) error
}
```

The `package` module executor uses this interface exclusively — it must not call any package manager binary directly.

---

## Compliance Reporting

The server maintains a continuous compliance view: for each client, is it currently in desired state? When did it last fully converge? How long has it been drifting?

### Compliance Status per Client

A client's compliance status is one of:
- `compliant` — last run was successful with no drift
- `drifted` — last run detected drift but auto-remediated it
- `failing` — last run had errors
- `stale` — has not checked in within `2 × base_interval`
- `unknown` — never completed a successful run

### Compliance API Endpoints

```
GET /api/v1/compliance/summary
  → { total, compliant, drifted, failing, stale, unknown, compliance_pct }

GET /api/v1/compliance/clients
  → list of clients with their current compliance status + last_seen + last_run_status

GET /api/v1/compliance/history?days=30
  → daily compliance percentage over the last N days (for dashboard chart)
```

### Web UI Addition

Add a **Compliance** view to the dashboard:
- Large compliance percentage gauge (e.g. "94.2% compliant")
- Breakdown donut chart by status category
- Table of non-compliant nodes with time-in-drift
- 30-day compliance trend line chart

### Prometheus Metrics Addition

```
hobnob_compliance_status{client,status}   # Gauge: 1 if client is in this status
hobnob_compliance_pct                     # Gauge: fleet-wide compliance percentage
hobnob_time_since_checkin_seconds{client} # Gauge: seconds since last check-in
```

---

## Updated Repository Structure

Add the following directories/files to the original structure:

```
hobnob/
├── agent/
│   ├── facts/                   # Fact collectors
│   │   ├── collector.go         # Collector interface
│   │   ├── os.go
│   │   ├── hardware.go
│   │   ├── network.go
│   │   └── packages.go
│   ├── modules/
│   │   └── pkgmgr/              # Package manager backends
│   │       ├── interface.go
│   │       ├── apt.go
│   │       ├── dnf.go
│   │       ├── apk.go
│   │       └── ...
│   └── snapshots/               # File snapshot manager
├── server/
│   ├── classifier/              # Auto node classification engine
│   ├── compliance/              # Compliance status tracker
│   ├── planner/                 # Dry-run plan computation
│   └── watcher/                 # isopass secret rotation watcher
└── config/
    └── classification.yaml      # Auto-classification rules
```

---

## Updated gRPC Service Definition

Extend `proto/hobnob.proto` with the additional RPCs:

```protobuf
service Hobnob {
  rpc Register(RegisterRequest) returns (RegisterResponse);
  rpc CheckIn(CheckInRequest) returns (CheckInResponse);
  rpc ReportResult(RunReport) returns (ReportAck);
  rpc ForcePush(ForcePushRequest) returns (ForcePushResponse);
  rpc DryRun(DryRunRequest) returns (DryRunResponse);         // dry-run / preview
  rpc Rollback(RollbackRequest) returns (RollbackResponse);   // file rollback
  rpc ReportFacts(FactsReport) returns (FactsAck);            // fact submission
}
```

The `CheckInRequest` must include the latest facts snapshot so the server always has current node state when computing the module list.

---

## Updated Implementation Order (Suggested)

1. **Read `~/repos/isopass` in full** — understand the API before writing anything else
2. `proto/hobnob.proto` — all services and messages including new RPCs
3. `server/secrets/` — `Provider` interface + `IsopassProvider` + `StaticProvider`
4. Database migrations — full schema including all new tables
5. Server skeleton — gRPC, TLS via secrets provider, DB pool
6. Registration logic — approved patterns, token validation, pubkey storage
7. Agent skeleton — keypair generation, registration flow
8. **Facts collection** — all fact collectors, `ReportFacts` RPC, storage
9. **Auto-classification** — `classification.yaml` loader + pattern matcher
10. CheckIn + module dispatch — YAML loading, `secret:` resolution, env var merging, DAG ordering
11. **Dry-run / planner** — `DryRun` RPC, plan computation, plan storage
12. Agent module executors — file (with snapshots), package (with multi-platform pkgmgr), service (notify/subscribe), user
13. **Conditional resources** — `when:` evaluation against facts
14. Agent run loop — jittered daemon, reporting, rollback handler
15. **Compliance tracker** — status computation, history table, compliance API
16. REST API + `hobcli` CLI (including all new subcommands)
17. Prometheus metrics (including new compliance metrics)
18. Webhook dispatcher (including new event types)
19. **Secret rotation watcher** — polling + optional isopass webhook endpoint
20. Web UI (including Plans, Compliance, and Package Audit views)
21. Docker Compose + Makefile + certs automation

---

## Updated Webhook Events

Add the following events to the original list:

- `plan.created` — a dry-run plan was generated
- `plan.changes_detected` — dry-run found pending changes
- `rollback.success` / `rollback.failure`
- `secret.rotated` — a watched secret was rotated (server detected new version)
- `compliance.degraded` — fleet compliance drops below a configurable threshold
- `package.version_drift` — a node's installed package version diverged from pinned version
- `node.stale` — a client has not checked in within `2 × base_interval`


---

## Design Philosophy

These principles take precedence over cleverness, flexibility, or feature completeness. When in doubt, choose the simpler path.

**Simplicity.** Every concept in Hobnob should be explainable in one sentence. If a feature requires a paragraph to explain how to use it, it needs to be redesigned. Prefer fewer, more composable primitives over many specialized ones.

**Clarity.** A sysadmin who has never seen Hobnob before should be able to read a client YAML file and immediately understand what it does. Naming must be unambiguous. Error messages must say what went wrong and what to do about it — never just a code or a stack trace.

**Conciseness.** Config files should contain only what is necessary. No boilerplate, no mandatory fields that are almost always the same value, no repetition between files. Defaults must be sensible enough that a minimal config is also a correct config.

---

## Single Configuration Path

There is exactly **one way** to configure a client on the server: a YAML file in `config/clients/`. No API calls, no database writes, no CLI flags, no UI forms. The filesystem is the source of truth.

- To add a client: create `config/clients/<hostname>.yaml`
- To change a client's modules: edit that file
- To remove a client's config: delete that file

The server watches the `config/clients/` directory for changes and reloads automatically. No restart required. `hobcli` never writes client config — it is read-only with respect to config files. Its role is operations (tokens, revocation, pushes, reports), not configuration.

Auto-classification rules in `config/classification.yaml` follow the same principle — one file, one format, edited directly.

If a client checks in and has no matching explicit config and no matching classification rule, it receives an empty module list and runs cleanly with no changes. It is never an error to be unconfigured.

---

## Hands-Off Client Operation

Once enrolled, a client node requires zero ongoing human interaction. The agent:

- Runs as a systemd service, started automatically on boot
- Self-heals: if the server is unreachable, it retries with exponential backoff and resumes normally when connectivity is restored
- Never requires a config file edit after initial enrollment
- Never requires a restart to pick up new modules — the server delivers the current desired state on every check-in
- Never requires manual intervention to rotate secrets — secret rotation is handled transparently via the isopass watcher
- Logs to journald; no log rotation setup required

The only time a human should need to touch a client node after enrollment is to upgrade the agent binary itself. Provide a `self-update` mechanism: the server exposes the latest agent binary at a versioned endpoint, and the agent checks for a newer version on each check-in and replaces itself if one is available (after verifying a checksum).

```
GET /api/v1/agent/latest          → { version, sha256, download_url }
GET /api/v1/agent/download        → binary stream
```

---

## Manual Client Run Command

The agent provides a one-shot run command for cases where a sysadmin wants to trigger an immediate convergence without waiting for the next scheduled check-in:

```bash
hobnob-agent run
```

This performs a single check-in, applies all modules, reports results, and exits. It does not affect the running daemon's schedule. Output goes to stdout so it is usable interactively.

Flags:

```bash
hobnob-agent run --dry-run          # preview only, no changes applied
hobnob-agent run --module <name>    # apply a single named module only
hobnob-agent run --verbose          # show per-resource detail
```

If the daemon is not running, `hobnob-agent run` works identically — it is fully self-contained and does not depend on the daemon being active.

---

## Bootstrap via Server-Hosted Script

The server hosts a bootstrap script at a well-known endpoint. Enrolling a new node requires exactly one command copy-pasted into a root shell:

```bash
curl -fsSL https://hobnob.example.com/bootstrap | bash -s -- --token <one-time-token>
```

Or with explicit options:

```bash
curl -fsSL https://hobnob.example.com/bootstrap | bash -s -- \
  --token <one-time-token> \
  --server hobnob.example.com:9090
```

### What the Bootstrap Script Does

The script is generated dynamically by the server and bakes in the server address and CA certificate automatically. When run on a new node it:

1. Detects the OS and architecture
2. Downloads the correct `hobnob-agent` binary from the server
3. Verifies the binary checksum
4. Writes `/etc/hobnob/agent.yaml` with the server address and CA cert
5. Writes the one-time token to `/etc/hobnob/token` (mode 0600)
6. Installs the agent as a systemd service (or launchd on macOS)
7. Starts the service — triggering immediate registration and first run
8. Cleans up the token file after successful enrollment
9. Prints a success message with the assigned client UUID

The script is plain POSIX shell with no dependencies beyond `curl` and `systemctl` (or `launchd`). It must work on any supported distro without installing additional tools.

### Bootstrap API Endpoints

```
GET /bootstrap
  → dynamically generated shell script with server address + CA cert baked in
  → Content-Type: text/plain

GET /api/v1/agent/latest
  → { version: "1.2.3", sha256: "...", platforms: { "linux/amd64": "/agent/download/linux/amd64", ... } }

GET /api/v1/agent/download/{os}/{arch}
  → binary stream of the hobnob-agent binary for the requested platform
```

The `/bootstrap` endpoint requires no authentication — the one-time token is passed as a flag to the downloaded script, not to the server at download time. The CA cert embedded in the script allows the agent to verify the server's TLS cert before the first gRPC call.

### hobcli Bootstrap Helpers

```bash
hobcli tokens generate
  → prints a new one-time token and the full bootstrap curl command ready to copy-paste

hobcli tokens generate --pattern "web-*"
  → generates token and auto-creates the approved_patterns entry in one step
```

This means enrolling a new node is a two-step process total:
1. Run `hobcli tokens generate --pattern "web-*"` on the server — copy the output
2. Paste the curl one-liner into a root shell on the new node

