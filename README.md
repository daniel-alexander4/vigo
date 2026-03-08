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
sudo cp certs/* /etc/hobnob/certs/
```

For production, replace with your own CA and server cert.

### 3. Create the `.env` file

```bash
sudo tee /etc/hobnob/.env <<'EOF'
HOBNOB_DB_DSN=postgres://hobnob:secret@postgres:5432/hobnob?sslmode=disable
HOBNOB_TLS_CERT=certs/server.crt
HOBNOB_TLS_KEY=certs/server.key
HOBNOB_TLS_CA=certs/ca.crt
EOF
```

### 4. Copy config files

```bash
sudo cp -r server-configs /etc/hobnob/
sudo cp -r client-configs /etc/hobnob/
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

### 6. Create an approved pattern and enrollment token

```bash
docker compose exec server hobcli tokens generate --pattern "*.example.com"
```

This prints a `curl` command with the enrollment token baked in.

### 7. Bootstrap a managed node

On each target node, run the `curl` command from the previous step:

```bash
curl http://<server-ip>:8080/bootstrap?token=<TOKEN> | bash
```

The bootstrap script detects OS/arch, downloads the agent binary, installs it, copies the CA cert, generates an ED25519 keypair, and enrolls with the server.

### 8. Define desired state

Edit `client-configs/clients.hob` to map hostnames to modules and roles:

```yaml
# client-configs/clients.hob
clients:
  - match: "web01.example.com"
    environment: production
    modules: [nginx]
    vars:
      doc_root: "/var/www/html"
```

Modules are defined in `client-configs/modules/`. For example, `client-configs/modules/nginx.hob`:

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

The `source` field references a template file relative to `client-configs/` (e.g. `client-configs/templates/nginx.conf.tmpl`). You can also use inline `content` — the two are mutually exclusive. Templates use Go template syntax with `.Traits` and `.Vars` namespaces.

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

Group modules into roles in `client-configs/roles/`:

```yaml
# client-configs/roles/webserver.hob
name: webserver
modules: [nginx, certbot]
```

The server watches `client-configs/` and hot-reloads automatically. On the next check-in, the agent converges to the desired state.

### 9. Verify convergence

```bash
# List enrolled clients
docker compose exec server hobcli clients list

# View run results
docker compose exec server hobcli runs list

# Check compliance
docker compose exec server hobcli clients show web01.example.com
```

Or open the web dashboard at `http://<server-ip>:8080/`.

## Config structure

```
client-configs/
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

Config files use the `.hob` extension (YAML syntax). The server also accepts `.yaml`/`.yml` for backwards compatibility. `clients.hob` entries use glob patterns — first match wins. Client-level `vars` override module defaults. Clients matching no entry get an empty module list.

## Examples

Ready-to-use module definitions in `client-configs/modules/` and role groupings in `client-configs/roles/`:

| Module | File | What it configures |
|--------|------|--------------------|
| Nginx | [`client-configs/modules/nginx.hob`](client-configs/modules/nginx.hob) | Virtual host, worker tuning, `source:` template |
| PostgreSQL | [`client-configs/modules/postgresql.hob`](client-configs/modules/postgresql.hob) | `postgresql.conf` + `pg_hba.conf`, memory tuning |
| MySQL | [`client-configs/modules/mysql.hob`](client-configs/modules/mysql.hob) | InnoDB tuning, slow query log, security hardening |
| Redis | [`client-configs/modules/redis.hob`](client-configs/modules/redis.hob) | LRU eviction, AOF persistence, `secret:` password |
| Docker | [`client-configs/modules/docker.hob`](client-configs/modules/docker.hob) | `daemon.json`, log rotation, containerd |
| HAProxy | [`client-configs/modules/haproxy.hob`](client-configs/modules/haproxy.hob) | Stats page, roundrobin backend, health checks |
| OpenSSH + fail2ban | [`client-configs/modules/openssh.hob`](client-configs/modules/openssh.hob), [`fail2ban.hob`](client-configs/modules/fail2ban.hob) | Hardened sshd, key-only auth, brute-force protection |
| Prometheus + Grafana | [`client-configs/modules/prometheus.hob`](client-configs/modules/prometheus.hob), [`grafana.hob`](client-configs/modules/grafana.hob) | Prometheus, node-exporter, Grafana |
| CLI tools | [`client-configs/modules/charm-tools.hob`](client-configs/modules/charm-tools.hob), [`github-tools.hob`](client-configs/modules/github-tools.hob) | `repository` for Charm repo, `archive` for GitHub releases |

Client-to-module mappings are in [`client-configs/clients.hob`](client-configs/clients.hob), with roles like `webserver`, `database`, `monitoring`, and `bastion` in [`client-configs/roles/`](client-configs/roles/).

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

- **Server** — gRPC endpoint for agents, REST API + web UI for operators
- **Agent** — daemon on each managed node, pulls config, converges idempotently
- **hobcli** — admin CLI, talks to server REST API
- **Config** — `.hob` files (YAML syntax) on disk, hot-reloaded on change
- **hobvi** — vim-based editor with syntax highlighting and idempotency validation on save

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
hobcli validate client-configs/modules/*.hob

# Allow non-idempotent resources (shows errors but exits 0)
hobcli validate --dangerous modules/risky.hob

# Edit with vim — syntax highlighting + validation on save
hobcli vi client-configs/modules/nginx.hob
# or via the hobvi symlink:
hobvi client-configs/modules/nginx.hob
```

`hobcli validate` checks: (1) YAML syntax, (2) required fields (`name`, resource `name`/`type`), (3) idempotency rules. `hobcli vi` opens vim with `.hob` syntax highlighting and blocks saves that fail validation.

## Key features

- **mTLS everywhere** — no plaintext gRPC connections
- **ED25519 auth** — every agent request is signature-verified
- **Dependency DAG** — resources declare `depends_on`, topologically sorted
- **Notify/subscribe** — config change triggers service restart
- **Conditional resources** — structured conditionals (`go_if_all_pass`, `skip_if_any_pass`, etc.) evaluated server-side
- **Idempotency enforcement** — `exec` resources require guards; validated at config load time
- **Nail collection** — OS, hardware, network, packages, identity, virtual, uptime, load, mountpoints, processors, DMI, cloud reported every check-in
- **Compliance tracking** — compliant/drifted/failing/stale/unknown per client
- **Secret rotation** — server polls isopass, force-runs affected clients
- **Webhooks** — HMAC-signed events for runs, drift, enrollment, rotation
- **Prometheus metrics** — `/metrics` endpoint with fleet-level gauges
- **Agent self-update** — automatic binary updates with checksum verification
- **Web dashboard** — fleet overview, client drill-down, compliance trends

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
# client-configs/clients.hob
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

### Using secrets in client configs

Reference secrets by path in `client-configs/clients.yaml` or module definitions:

```yaml
# client-configs/clients.hob
clients:
  - match: "cache01.example.com"
    modules: [redis]
    vars:
      redis_password: "secret:hobnob/redis/password"
```

```yaml
# client-configs/modules/grafana.hob (inline in resource attributes)
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

The server also polls isopass for secret version changes. When a secret rotates, all clients referencing it are automatically queued for re-convergence.

**StaticProvider (development)** — reads from a `.env` file. Secret paths are converted to environment variable names: `secret:hobnob/db/dsn` → `HOBNOB_DB_DSN` (uppercase, slashes to underscores).

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
