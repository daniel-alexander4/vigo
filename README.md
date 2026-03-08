# Hobnob

Pull-based configuration management for Linux and macOS. Agents on managed nodes pull desired state from a central server via gRPC over mTLS, apply idempotent changes (files, packages, services, users), and report results back.

## Quickstart

### 1. Create `/etc/hobnob`

All server config lives in `/etc/hobnob` on the host. The container bind-mounts it read-only.

```bash
sudo mkdir -p /etc/hobnob/certs
```

### 2. Generate TLS certificates

```bash
make certs
sudo cp server-configs/certs/* /etc/hobnob/certs/
```

For production, replace with your own CA and server cert. Keep the CA private key accessible to the server if you want bootstrap CSR signing (see [Bootstrap enrollment](#bootstrap-enrollment)).

### 3. Create the `.env` file

```bash
sudo tee /etc/hobnob/.env <<'EOF'
HOBNOB_DB_DSN=postgres://hobnob:secret@postgres:5432/hobnob?sslmode=disable
HOBNOB_TLS_CERT=certs/server.crt
HOBNOB_TLS_KEY=certs/server.key
HOBNOB_TLS_CA=certs/ca.crt
HOBNOB_TLS_CA_KEY=certs/ca.key
EOF
```

### 4. Copy config files

```bash
sudo cp -r server-configs /etc/hobnob/
sudo cp -r node-configs /etc/hobnob/
```

### 5. Start the server

```bash
docker compose up -d
```

This starts PostgreSQL and the hobnob server. Migrations run automatically. Verify:

```bash
docker compose ps
```

You should see `postgres` (healthy) and `server` (running). The dashboard is at `http://<server-ip>:8080/`.

### 6. Bootstrap managed nodes

There are two enrollment modes: **trusted enrollment** (zero-touch, like Puppet autosigning) and **token-based** (explicit per-node approval).

#### Trusted enrollment (recommended for fleets)

Configure trusted hostname/CIDR patterns in `server-configs/server.yaml`:

```yaml
bootstrap:
  cert_validity: "8760h"
  trusted_enrollment:
    - pattern: "*.web.example.com"
      cidrs: ["10.0.1.0/24"]
    - pattern: "*.db.internal"
      cidrs: ["10.0.2.0/24"]
```

Then on each node, run:

```bash
curl -sSf http://<server-ip>:8080/bootstrap | HOBNOB_SERVER=http://<server-ip>:8080 sh
```

Or use the standalone bootstrap script:

```bash
HOBNOB_SERVER=http://<server-ip>:8080 sh scripts/bootstrap.sh
```

No token needed. The server validates the node's hostname and source IP against the trusted patterns, signs its CSR, and returns TLS certs. The agent is installed, enrolled, and started as a systemd service.

#### Token-based enrollment

For one-off nodes or untrusted networks, generate a token:

```bash
hobcli tokens generate
hobcli approved add "web-01.example.com" --token <TOKEN>
```

Then on the node:

```bash
HOBNOB_SERVER=http://<server-ip>:8080 HOBNOB_TOKEN=<TOKEN> sh scripts/bootstrap.sh
```

Or use the server-generated bootstrap script:

```bash
curl -sSf "http://<server-ip>:8080/bootstrap?token=<TOKEN>" | sh
```

#### What bootstrap does

1. Downloads the `hob` agent binary for the node's OS/arch
2. Generates an ED25519 keypair (for request signing) and an ECDSA TLS keypair
3. Creates a CSR and sends it to the server
4. Server validates (trusted pattern + CIDR, or token + approved pattern)
5. Server signs the CSR and returns the signed TLS cert + CA cert
6. Agent saves certs, keypair, and node UUID to `/etc/hobnob`
7. Installs and starts a systemd service

After bootstrap, the agent connects to the server via mTLS automatically.

### 7. Define desired state

Edit `node-configs/clients.hob` to map hostnames to modules and roles:

```yaml
# node-configs/clients.hob
clients:
  - match: "web01.example.com"
    environment: production
    modules: [nginx]
    vars:
      doc_root: "/var/www/html"
```

Modules are defined in `node-configs/modules/`. For example, `node-configs/modules/nginx.hob`:

```yaml
name: nginx
vars:
  worker_processes: "auto"
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present

  - name: nginx-conf
    type: file
    targetpath: /etc/nginx/nginx.conf
    source: templates/nginx.conf.tmpl
    owner: root
    mode: "0644"
    depends_on: [install-nginx]
    notify: [nginx-service]

  - name: nginx-service
    type: service
    service: nginx
    enabled: true
    state: running
    subscribes: [nginx-conf]
```

The `source` field references a template file relative to `node-configs/` (e.g. `node-configs/templates/nginx.conf.tmpl`). You can also use inline `content` — the two are mutually exclusive. Templates use Go template syntax with `.Traits` and `.Vars` namespaces.

Use `baseindent` to prepend a fixed number of spaces to every non-empty line of `content`. Relative indentation within the content block is preserved:

```yaml
  - name: app-conf
    type: file
    targetpath: /etc/myapp/config.yml
    baseindent: 2
    content: |
      database:
        host: localhost
        port: 5432
      cache:
        ttl: 300
```

This writes each line with 2 extra leading spaces (4 for the already-indented lines), while empty lines remain empty. Defaults to 0 (no extra indent).

Group modules into roles in `node-configs/roles/`:

```yaml
# node-configs/roles/webserver.hob
name: webserver
modules: [nginx, certbot]
```

The server watches `node-configs/` and hot-reloads automatically. On the next check-in, the agent converges to the desired state.

### 8. Verify convergence

```bash
# List enrolled nodes
hobcli nodes list

# View run results
hobcli runs list

# Check compliance
hobcli nodes show web01.example.com
```

Or open the web dashboard at `http://<server-ip>:8080/`.

## Config structure

```
node-configs/
├── clients.hob           # Maps hostnames/globs to roles and modules
├── modules/              # Reusable resource sets with default vars
│   ├── nginx.hob
│   ├── postgresql.hob
│   └── ...
├── roles/                # Named groups of modules
│   ├── webserver.hob
│   ├── database.hob
│   └── ...
└── templates/            # External template files (referenced via source:)
    └── nginx.conf.tmpl
```

Config files use the `.hob` extension (YAML syntax). The server also accepts `.yaml`/`.yml`. `clients.hob` entries use glob patterns — first match wins. Client-level `vars` override module defaults. Nodes matching no entry get an empty module list.

## Examples

Ready-to-use module definitions in `node-configs/modules/` and role groupings in `node-configs/roles/`:

| Module | File | What it configures |
|--------|------|--------------------|
| Nginx | `node-configs/modules/nginx.hob` | Virtual host, worker tuning, `source:` template |
| PostgreSQL | `node-configs/modules/postgresql.hob` | `postgresql.conf` + `pg_hba.conf`, memory tuning |
| MySQL | `node-configs/modules/mysql.hob` | InnoDB tuning, slow query log, security hardening |
| Redis | `node-configs/modules/redis.hob` | LRU eviction, AOF persistence, `secret:` password |
| Docker | `node-configs/modules/docker.hob` | `daemon.json`, log rotation, containerd |
| HAProxy | `node-configs/modules/haproxy.hob` | Stats page, roundrobin backend, health checks |
| OpenSSH + fail2ban | `node-configs/modules/openssh.hob`, `node-configs/modules/fail2ban.hob` | Hardened sshd, key-only auth, brute-force protection |
| Prometheus + Grafana | `node-configs/modules/prometheus.hob`, `node-configs/modules/grafana.hob` | Prometheus, node-exporter, Grafana |
| CLI tools | `node-configs/modules/charm-tools.hob`, `node-configs/modules/github-tools.hob` | `repository` for Charm repo, `archive` for GitHub releases |

Node-to-module mappings are in `node-configs/clients.hob`, with roles like `webserver`, `database`, `monitoring`, and `bastion` in `node-configs/roles/`.

Each example demonstrates resource wiring (`depends_on`, `notify`, `subscribes`), Go template variables, per-environment overrides, and `secret:` references where appropriate.

## Architecture

```
┌──────────────┐     gRPC/mTLS     ┌──────────────┐
│   Agent       │◄─────────────────►│   Server      │
│  (per node)   │     pull loop     │  :9090 gRPC   │
│               │                   │  :8080 REST   │
└──────────────┘                   └──────┬───────┘
                                          │
                            ┌─────────────┼─────────────┐
                            │             │             │
                       ┌────▼───┐   ┌─────▼────┐  ┌────▼───┐
                       │Postgres│   │  Config   │  │isopass │
                       │  :5432 │   │  (YAML)   │  │(secrets)│
                       └────────┘   └──────────┘  └────────┘
```

- **Server (hobd)** — gRPC endpoint for agents, REST API + web UI for operators, built-in CA for bootstrap CSR signing
- **Agent (hob)** — daemon on each managed node, pulls config, converges idempotently
- **hobcli** — admin CLI, talks to server REST API
- **Config** — `.hob` files (YAML syntax) on disk, hot-reloaded on change
- **hobvi** — vim-based editor with syntax highlighting and idempotency validation on save

## Bootstrap enrollment

Hobnob supports two enrollment modes. Both use CSR signing — the server acts as a certificate authority, signs the agent's CSR, and returns TLS certificates for future mTLS connections.

### Trusted enrollment (autosigning)

Like Puppet's autosign, the server auto-enrolls nodes matching configured hostname patterns + CIDR ranges. Configure in `server-configs/server.yaml`:

```yaml
server:
  tls:
    ca_key_file: "secret:hobnob/tls/ca_key"  # required for CSR signing

bootstrap:
  cert_validity: "8760h"  # 1 year
  trusted_enrollment:
    - pattern: "*.web.example.com"
      cidrs: ["10.0.1.0/24"]
    - pattern: "*.db.internal"
      cidrs: ["10.0.2.0/24", "172.16.0.0/16"]
    - pattern: "monitoring-*"
      # no cidrs = any source IP (less secure)
```

Security comes from two layers: hostname pattern matching and source IP CIDR validation. Omitting `cidrs` allows any source IP (use only on trusted networks).

### Token-based enrollment

For higher-security environments or one-off nodes. Requires an admin to generate a one-time token and approved pattern before the node can enroll.

### Bootstrap flow

```
Node                              Server
│                                 │
├─ hob bootstrap --server <addr>  │
│  ├─ Generate ED25519 keypair    │
│  ├─ Generate ECDSA TLS key     │
│  ├─ Create CSR                 │
│  │                              │
│  ├─ POST /api/v1/bootstrap/register ─────────►
│  │   {hostname, ip, pubkey, csr, token?}      │
│  │                              │  Validate:
│  │                              │  ├─ Token + pattern match
│  │                              │  │  OR trusted pattern + CIDR
│  │                              │  ├─ Sign CSR with CA key
│  │                              │  └─ Create node record
│  │  ◄──────── {node_id, signed_cert, ca_cert} │
│  │                              │
│  ├─ Save cert, key, CA, UUID   │
│  └─ Start mTLS daemon loop ────►│
│                                 │
```

## Resource types

| Type | What it manages | Idempotency check |
|------|----------------|-------------------|
| `file` | File content, owner, group, mode, base indent | SHA256 hash comparison |
| `package` | Package installation/removal | Query package manager |
| `service` | Service state (running/stopped/restarted) | `systemctl` status |
| `user` | User accounts and attributes | `/etc/passwd` lookup |
| `repository` | Third-party package repos + GPG keys | Check repo config file exists |
| `archive` | Download + install binaries from URLs | SHA256 hash or `unless` command |
| `exec` | Arbitrary commands | `creates`/`onlyif`/`unless` guards |
| `cron` | Cron jobs via `/etc/cron.d/` drop-ins | File content hash comparison |

## Idempotency validation

Hobnob enforces idempotency at config load time. Every resource must be provably idempotent before the server will accept it:

- **Inherently idempotent** — `file`, `package`, `service`, `user`, `repository`, `archive`, `cron` all check current state before acting
- **Requires a guard** — `exec` must declare at least one of `creates`, `onlyif`, or `unless`
- **Warning** — `service` with `state: restarted`/`reloaded` without `subscribes` triggers a warning (runs every convergence)

Non-idempotent resources are rejected unless the server is started with `--dangerous` or `HOBNOB_DANGEROUS=1`.

### Validation tools

```bash
# Validate .hob files for YAML correctness + idempotency
hobcli validate node-configs/modules/*.hob

# Allow non-idempotent resources (shows errors but exits 0)
hobcli validate --dangerous modules/risky.hob

# Edit with vim — syntax highlighting + validation on save
hobcli vi node-configs/modules/nginx.hob
# or via the hobvi symlink:
hobvi node-configs/modules/nginx.hob
```

`hobcli validate` checks: (1) YAML syntax, (2) required fields (`name`, resource `name`/`type`), (3) idempotency rules. `hobcli vi` opens vim with `.hob` syntax highlighting and blocks saves that fail validation.

## Key features

- **Bootstrap enrollment** — CSR-based: server signs agent certs, no pre-distribution needed
- **Trusted enrollment** — Puppet-style autosigning with hostname patterns + CIDR restrictions
- **mTLS everywhere** — no plaintext gRPC connections
- **ED25519 auth** — every agent request is signature-verified
- **Dependency DAG** — resources declare `depends_on`, topologically sorted
- **Notify/subscribe** — config change triggers service restart
- **Conditional resources** — structured conditionals (`go_if_all_pass`, `skip_if_any_pass`, etc.) evaluated server-side
- **Idempotency enforcement** — `exec` resources require guards; validated at config load time
- **Nail collection** — OS, hardware, network, packages, time, identity, virtual, uptime, load, mountpoints, processors, DMI, cloud reported every check-in
- **Compliance tracking** — compliant/drifted/failing/stale/unknown per node
- **Secret rotation** — server polls isopass, force-runs affected nodes
- **Webhooks** — HMAC-signed events for runs, drift, enrollment, rotation
- **Prometheus metrics** — `/metrics` endpoint with fleet-level gauges
- **Agent self-update** — automatic binary updates with checksum verification
- **Web dashboard** — fleet overview, node drill-down, compliance trends

## Conditionals

Conditionals control which resources and modules run on a given node. They are evaluated **server-side** — agents never see skipped resources. The system has three layers: conditional vars, resource/module conditionals, and conditional blocks.

### Conditional vars

A conditional var is a structured YAML variable that evaluates a node trait and produces a boolean (`"true"` or `"false"`). Define them in `vars` at the module, client, or environment level:

```yaml
vars:
  is_debian:
    trait: os.distro
    only: [debian, ubuntu]

  is_not_container:
    trait: hardware.virtualization
    not: [docker, lxc]

  has_enough_ram:
    trait: hardware.memory_mb
    gte: 2048

  hostname_matches_web:
    trait: os.hostname
    match: "^web\\d+\\."
```

Each conditional var requires a `trait` field (dot-path into the node's traits map) and exactly one operator:

| Operator | Operand type | True when |
|----------|-------------|-----------|
| `only` | list | trait value is in the list |
| `not` | list | trait value is NOT in the list |
| `gt` | number | trait > operand |
| `lt` | number | trait < operand |
| `gte` | number | trait >= operand |
| `lte` | number | trait <= operand |
| `match` | regex string | trait matches the regex |

Missing traits evaluate to `false`. Conditional vars are evaluated before template rendering, so their boolean results are available to other vars and to conditional fields.

### Resource conditionals

Resources use four fields to reference conditional vars. Evaluation follows a **gate-then-veto** pattern:

```yaml
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
    go_if_all_pass: [is_debian, has_enough_ram]   # gate: ALL must be true

  - name: install-httpd
    type: package
    package: httpd
    state: present
    go_if_any_pass: [is_rhel, is_centos]          # gate: at least ONE must be true

  - name: dev-tools
    type: package
    package: strace, htop
    state: present
    skip_if_all_pass: [is_production, is_locked]   # veto: skip if ALL are true

  - name: debug-config
    type: file
    targetpath: /etc/myapp/debug.conf
    content: "debug = true"
    skip_if_any_pass: is_production                # veto: skip if ANY is true
```

| Field | Behavior |
|-------|----------|
| `go_if_all_pass` | Gate — ALL listed vars must be `"true"`, otherwise resource is skipped |
| `go_if_any_pass` | Gate — at least ONE listed var must be `"true"`, otherwise skipped |
| `skip_if_all_pass` | Veto — if ALL listed vars are `"true"`, resource is skipped |
| `skip_if_any_pass` | Veto — if ANY listed var is `"true"`, resource is skipped |

Gates are checked first, then vetoes. Each field accepts a single string or a list.

### Module conditionals

The same four fields work on module references in `clients.hob` and role definitions, skipping entire modules:

```yaml
# node-configs/clients.hob
clients:
  - match: "*.example.com"
    vars:
      is_debian:
        trait: os.distro
        only: [debian, ubuntu]
    modules:
      - nginx
      - name: apt-cacher
        go_if_all_pass: is_debian
      - name: selinux-policy
        skip_if_any_pass: is_debian
```

Plain string module references (like `nginx` above) are always included. The object form with `name:` allows attaching conditional fields.

### Conditional blocks

When multiple resources share the same conditions, use `conditional_block` to avoid repetition:

```yaml
resources:
  - name: always-runs
    type: package
    package: curl
    state: present

  - conditional_block:
      go_if_all_pass: is_debian
      resources:
        - name: apt-transport-https
          type: package
          package: apt-transport-https
          state: present

        - name: apt-conf
          type: file
          targetpath: /etc/apt/apt.conf.d/99custom
          content: |
            APT::Install-Recommends "false";

  - name: also-always-runs
    type: package
    package: wget
    state: present
```

Block-level conditionals are merged onto each child resource at config load time — the block flattens away. If a child resource also has its own conditional fields, both sets apply (block conditions are prepended).

### Full example

Putting it all together — a module that adapts to OS and hardware:

```yaml
name: webserver

vars:
  is_debian:
    trait: os.distro
    only: [debian, ubuntu]
  is_rhel:
    trait: os.distro
    only: [rhel, centos, rocky, alma]
  has_enough_ram:
    trait: hardware.memory_mb
    gte: 1024
  worker_processes: "auto"

resources:
  - name: install-nginx-deb
    type: package
    package: nginx
    state: present
    go_if_all_pass: is_debian

  - name: install-nginx-rpm
    type: package
    package: nginx
    state: present
    go_if_all_pass: is_rhel

  - name: nginx-conf
    type: file
    targetpath: /etc/nginx/nginx.conf
    source: templates/nginx.conf.tmpl
    mode: "0644"
    notify: [nginx-service]

  - name: cache-tuning
    type: file
    targetpath: /etc/nginx/conf.d/cache.conf
    content: |
      proxy_cache_path /var/cache/nginx levels=1:2
        keys_zone=default:10m max_size=1g;
    go_if_all_pass: has_enough_ram

  - name: nginx-service
    type: service
    service: nginx
    enabled: true
    state: running
    subscribes: [nginx-conf]
```

## Secrets

Sensitive values in config files use the `secret:` prefix instead of plaintext. The server resolves these references at config load time before sending anything to agents — agents receive the resolved values, never the `secret:` references.

### Using secrets in node configs

Reference secrets by path in `node-configs/clients.hob` or module definitions:

```yaml
# node-configs/clients.hob
clients:
  - match: "cache01.example.com"
    modules: [redis]
    vars:
      redis_password: "secret:hobnob/redis/password"
```

```yaml
# node-configs/modules/grafana.hob (inline in resource attributes)
resources:
  - name: grafana-ini
    type: file
    targetpath: /etc/grafana/grafana.ini
    content: |
      admin_password = secret:hobnob/grafana/admin-password
```

Certain field names (`dsn`, `tls_cert_path`, `tls_key_path`, `webhook_secret`) are enforced — the server rejects configs that set these to plaintext values.

### Secrets providers

Hobnob supports two secret backends via the `secrets.Provider` interface:

**IsopassProvider (production)** — retrieves secrets from an [isopass](https://github.com/dansheldon/isopass) instance over HTTP. Authenticates with a service account bearer token, exchanges it for a short-lived API token, and searches for secrets by path. Configure with:

```bash
ISOPASS_URL=https://isopass.internal:8443
ISOPASS_BEARER_TOKEN=<service-account-token>
```

The server also polls isopass for secret version changes. When a secret rotates, all nodes referencing it are automatically queued for re-convergence.

**StaticProvider (development)** — reads from a `.env` file. Secret paths are converted to environment variable names: `secret:hobnob/db/dsn` -> `HOBNOB_DB_DSN` (uppercase, slashes to underscores).

### Security guarantees

- Secrets never appear in plaintext in config files, logs, gRPC payloads, or run results
- If the secrets provider is unreachable, the server fails fast — no fallback to insecure defaults
- Resolved secret values are wrapped in `SecretString`, which prints `[REDACTED]` on `String()`

## Environment configuration

The server's own config (`server-configs/server.yaml`) also uses `secret:` references, resolved from `.env` via the StaticProvider:

| `.env` variable | Required | Description |
|-----------------|----------|-------------|
| `HOBNOB_DB_DSN` | Yes | PostgreSQL connection string |
| `HOBNOB_TLS_CERT` | Yes | Path to server TLS certificate |
| `HOBNOB_TLS_KEY` | Yes | Path to server TLS private key |
| `HOBNOB_TLS_CA` | Yes | Path to CA certificate |
| `HOBNOB_TLS_CA_KEY` | No | Path to CA private key (enables bootstrap CSR signing) |
| `HOBNOB_WEBHOOK_SECRET` | No | HMAC secret for webhook signing |
| `HOBNOB_VERSION` | No | Version string for agent self-update |
| `HOBNOB_AGENT_DIR` | No | Directory with cross-compiled agent binaries |

## Development

```bash
make build          # Build server, agent, hobcli + hobvi symlink
make proto          # Regenerate protobuf/gRPC code
make certs          # Generate self-signed TLS certs
make test           # Run all tests
make lint           # Run linter
make dist           # Cross-compile agent binaries
make seed           # Insert dev seed data
docker compose up   # Start server + postgres
```
