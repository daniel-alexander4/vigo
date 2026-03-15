# Hobnob

Pull-based configuration management for Linux and macOS. Agents on managed nodes pull desired state from a central server via gRPC over mTLS, apply idempotent changes (files, packages, services, users), and report results back.

## Quickstart

### 1. Start the server

```bash
sudo mkdir -p /srv/hobnob
docker run --rm -v /srv/hobnob:/srv/hobnob ghcr.io/daniel-alexander4/hobnob:latest --seed-only
cd /srv/hobnob
docker compose up -d
```

The first command seeds `/srv/hobnob/` with `docker-compose.yml`, `server.yaml`, `.env`, TLS certificates, and example configs. The second starts the server. Dashboard at `http://localhost:8181/`.

### 2. Bootstrap managed nodes

On each node you want to manage, run:

```bash
curl -sSf http://<server-ip>:8181/bootstrap | sudo sh
```

That's it. The server generates a shell script on the fly that downloads the `hobnob` agent binary for the node's OS/arch, enrolls it, and starts it as a systemd service. No Go, no source code, no package manager needed on the node.

#### What the bootstrap curl does

1. Downloads the pre-built `hobnob` binary from the server to `/usr/local/sbin/hobnob`
2. Runs `hobnob bootstrap` which generates an ED25519 keypair + ECDSA TLS keypair
3. Sends a CSR to the server
4. Server validates the node (trusted pattern + CIDR, or token)
5. Server signs the CSR and returns the signed TLS cert + CA cert
6. Agent saves certs, keypair, and node UUID to `/etc/hobnob-node`
7. Creates and starts the `hobnob-node` systemd service

After bootstrap, the agent checks in with the server via mTLS every 5 minutes.

#### Enrollment modes

**Trusted enrollment (default)** — the server auto-enrolls nodes matching configured hostname patterns and CIDR ranges, no token needed. Configure in `server-configs/server.yaml`:

```yaml
bootstrap:
  cert_validity: "8760h"
  trusted_enrollment:
    - pattern: "*.web.example.com"
      cidrs: ["10.0.1.0/24"]
    - pattern: "*.db.internal"
      cidrs: ["10.0.2.0/24"]
```

The default config accepts any hostname from RFC 1918 / loopback addresses.

**Token-based enrollment** — for untrusted networks or explicit per-node approval:

```bash
# On the server:
hobnobcli tokens generate
hobnobcli approved add "web-01.example.com" --token <TOKEN>

# On the node:
curl -sSf "http://<server-ip>:8181/bootstrap?token=<TOKEN>" | sudo sh
```

#### Factory reset a node

To wipe all agent state and re-bootstrap from scratch:

```bash
sudo scripts/reset-node.sh
```

This stops the service, removes the binary, and deletes `/etc/hobnob-node`. Then bootstrap again with `curl`.

### 7. Define desired state

Edit `thrall-configs/nodes.hnb` to map hostnames to modules and roles:

```yaml
# thrall-configs/nodes.hnb
nodes:
  - match: "web01.example.com"
    environment: production
    modules: [nginx]
    vars:
      doc_root: "/var/www/html"
```

Modules are defined in `thrall-configs/modules/`. For example, `thrall-configs/modules/nginx.hnb`:

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

The `source` field references a template file relative to `thrall-configs/` (e.g. `thrall-configs/templates/nginx.conf.tmpl`). You can also use inline `content` — the two are mutually exclusive. Templates use Go template syntax with `.Traits` and `.Vars` namespaces.

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

Use `stream_edit` to pipe content through a script (or chain of scripts) on the thrall before writing. Each script reads stdin and writes transformed output to stdout:

```yaml
  - name: app-conf
    type: file
    targetpath: /etc/myapp/config.conf
    content: |
      # database settings
      host = localhost
      port = 5432
    stream_edit: /usr/local/bin/strip-comments.sh
```

For chained transforms, pass a list — scripts execute left-to-right as a pipeline:

```yaml
    stream_edit:
      - /usr/local/bin/strip-comments.sh
      - /usr/local/bin/sort-dedup.sh
```

Scripts must already exist on the thrall. Deploy them with a file resource and `depends_on`. Pipeline results are cached — scripts only re-run when content, script files, or the target file change. Non-zero exit fails the resource (no file written). See [file executor docs](docs/executors/file.md#stream-edit) for details and example scripts.

Group modules into roles in `thrall-configs/roles/`:

```yaml
# thrall-configs/roles/webserver.hnb
name: webserver
modules: [nginx, certbot]
```

The `config publish` command validates, syncs, and reloads the server. On the next check-in, the agent converges to the desired state.

### 8. Verify convergence

```bash
# List enrolled nodes
hobnobcli nodes list

# View run results
hobnobcli runs list

# Check compliance
hobnobcli nodes show web01.example.com
```

Or open the web dashboard at `http://<server-ip>:8181/`.

## Config structure

```
thrall-configs/
├── nodes.hnb             # Maps hostnames/globs to roles and modules
├── modules/              # Reusable resource sets with default vars
│   ├── nginx.hnb
│   ├── postgresql.hnb
│   └── ...
├── roles/                # Named groups of modules
│   ├── webserver.hnb
│   ├── database.hnb
│   └── ...
└── templates/            # External template files (referenced via source:)
    └── nginx.conf.tmpl
```

Config files use the `.hnb` extension (YAML syntax). The server also accepts `.yaml`/`.yml`. `nodes.hnb` entries use glob patterns — first match wins. Node-level `vars` override module defaults. Nodes matching no entry get an empty module list.

## Examples

Ready-to-use module definitions in `thrall-configs/modules/` and role groupings in `thrall-configs/roles/`:

| Module | File | What it configures |
|--------|------|--------------------|
| Nginx | `thrall-configs/modules/nginx.hnb` | Virtual host, worker tuning, `source:` template |
| PostgreSQL | `thrall-configs/modules/postgresql.hnb` | `postgresql.conf` + `pg_hba.conf`, memory tuning |
| MySQL | `thrall-configs/modules/mysql.hnb` | InnoDB tuning, slow query log, security hardening |
| Redis | `thrall-configs/modules/redis.hnb` | LRU eviction, AOF persistence, `secret:` password |
| Docker | `thrall-configs/modules/docker.hnb` | `daemon.json`, log rotation, containerd |
| HAProxy | `thrall-configs/modules/haproxy.hnb` | Stats page, roundrobin backend, health checks |
| OpenSSH + fail2ban | `thrall-configs/modules/openssh.hnb`, `thrall-configs/modules/fail2ban.hnb` | Hardened sshd, key-only auth, brute-force protection |
| Prometheus + Grafana | `thrall-configs/modules/prometheus.hnb`, `thrall-configs/modules/grafana.hnb` | Prometheus, node-exporter, Grafana |
| CLI tools | `thrall-configs/modules/charm-tools.hnb`, `thrall-configs/modules/github-tools.hnb` | `repository` for Charm repo, `archive` for GitHub releases |

Node-to-module mappings are in `thrall-configs/nodes.hnb`, with roles like `webserver`, `database`, `monitoring`, and `bastion` in `thrall-configs/roles/`.

Each example demonstrates resource wiring (`depends_on`, `notify`, `subscribes`), Go template variables, per-environment overrides, and `secret:` references where appropriate.

## Architecture

```
┌──────────────┐     gRPC/mTLS     ┌──────────────┐
│   Agent       │◄─────────────────►│   Server      │
│  (per node)   │     pull loop     │  :1530 gRPC   │
│               │                   │  :8181 REST   │
└──────────────┘                   └──────┬───────┘
                                          │
                            ┌─────────────┼─────────────┐
                            │             │             │
                       ┌────▼───┐   ┌─────▼────┐  ┌────▼───┐
                       │ SQLite │   │  Config   │  │isopass │
                       │  (WAL) │   │  (YAML)   │  │(secrets)│
                       └────────┘   └──────────┘  └────────┘
```

**Port 1530** — approximate first recorded written use of "hobgoblin" in English. Clean, short, historically grounded.

- **Server (hobd)** — gRPC endpoint for agents, REST API + web UI for operators, built-in CA for bootstrap CSR signing
- **Agent (hobnob)** — daemon on each managed node, pulls config, converges idempotently
- **hobnobcli** — admin CLI, talks to server REST API
- **Config** — `.hnb` files (YAML syntax) on disk, reloaded on publish
- **hnbvi** — vim-based editor with syntax highlighting and idempotency validation on save

## Bootstrap enrollment

Hobnob supports two enrollment modes. Both use CSR signing — the server acts as a certificate authority, signs the agent's CSR, and returns TLS certificates for future mTLS connections.

### Trusted enrollment (autosigning)

The server auto-enrolls nodes matching configured hostname patterns + CIDR ranges. Configure in `server-configs/server.yaml`:

```yaml
server:
  tls:
    ca_key_file: "tls/ca_key"  # required for CSR signing

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
├─ hobnob bootstrap --server <addr>  │
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
- **Error** — `service` with `state: restarted`/`reloaded` without `subscribes` is rejected (runs every convergence)

Non-idempotent resources are always rejected. Use `skip_validation: true` on individual resources to bypass validation for custom executors.

### Validation tools

```bash
# Validate .hnb files for YAML correctness + idempotency
hobnobcli validate thrall-configs/modules/*.hnb

# Edit with vim — syntax highlighting + validation on save
hobnobcli vi thrall-configs/modules/nginx.hnb
# or via the hnbvi symlink:
hnbvi thrall-configs/modules/nginx.hnb
```

`hobnobcli validate` checks: (1) YAML syntax, (2) required fields (`name`, resource `name`/`type`), (3) idempotency rules. `hobnobcli vi` opens vim with `.hnb` syntax highlighting and blocks saves that fail validation.

## Key features

- **Bootstrap enrollment** — CSR-based: server signs agent certs, no pre-distribution needed
- **Trusted enrollment** — autosigning with hostname patterns + CIDR restrictions
- **mTLS everywhere** — no plaintext gRPC connections
- **ED25519 auth** — every agent request is signature-verified
- **Dependency DAG** — resources declare `depends_on`, topologically sorted
- **Notify/subscribe** — config change triggers service restart
- **Conditional resources** — `when:` expressions with boolean logic and builtin functions, evaluated server-side
- **Idempotency enforcement** — `exec` resources require guards; validated at config load time
- **Trait collection** — OS, hardware, network, packages, time, identity, virtual, uptime, load, mountpoints, processors, DMI, cloud reported every check-in
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

Resources use `when:` to control whether they run on a given node. The expression is a boolean formula over conditional vars and builtin functions:

```yaml
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
    when: "is_debian and has_enough_ram"

  - name: install-httpd
    type: package
    package: httpd
    state: present
    when: "is_rhel or is_centos"

  - name: dev-tools
    type: package
    package: strace, htop
    state: present
    when: "not (is_production and is_locked)"

  - name: debug-config
    type: file
    targetpath: /etc/myapp/debug.conf
    content: "debug = true"
    when: "not is_production"
```

Expressions support `and`, `or`, `not`, parentheses, and builtin function calls. Var references resolve to `"true"`/`"false"` from conditional vars. Builtin functions (e.g., `os_family('debian')`, `file_exists('/path')`) that depend on agent-side state are passed through to the agent for evaluation.

### Module conditionals

`when:` also works on module references in `nodes.hnb` and role definitions, skipping entire modules:

```yaml
# thrall-configs/nodes.hnb
nodes:
  - match: "*.example.com"
    vars:
      is_debian:
        trait: os.distro
        only: [debian, ubuntu]
    modules:
      - nginx
      - name: apt-cacher
        when: "is_debian"
      - name: selinux-policy
        when: "not is_debian"
```

Plain string module references (like `nginx` above) are always included. The object form with `name:` allows attaching `when:` expressions.

### Conditional blocks

When multiple resources share the same condition, use `conditional_block` to avoid repetition:

```yaml
resources:
  - name: always-runs
    type: package
    package: curl
    state: present

  - conditional_block:
      when: "is_debian"
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

Block-level `when:` is merged onto each child resource at config load time — the block flattens away. If a child resource also has its own `when:`, both are combined with `and`.

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
    when: "is_debian"

  - name: install-nginx-rpm
    type: package
    package: nginx
    state: present
    when: "is_rhel"

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
    when: "has_enough_ram"

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

Reference secrets by path in `thrall-configs/nodes.hnb` or module definitions:

```yaml
# thrall-configs/nodes.hnb
nodes:
  - match: "cache01.example.com"
    modules: [redis]
    vars:
      redis_password: "secret:hobnob/redis/password"
```

```yaml
# thrall-configs/modules/grafana.hnb (secret in var, referenced in template)
vars:
  grafana_admin_password: "secret:hobnob/grafana/admin-password"

resources:
  - name: grafana-ini
    type: file
    targetpath: /etc/grafana/grafana.ini
    content: |
      admin_password = {{ .Vars.grafana_admin_password }}
```

The `secret:` prefix is resolved on **var values** and **resource attributes**, not inside template strings. To use a secret in a `content:` template, put the `secret:` reference in a var and use `{{ .Vars.name }}`.

Certain field names (`dsn`, `tls_cert_path`, `tls_key_path`, `webhook_secret`) are enforced — the server rejects configs that set these to plaintext values.

### Secrets providers

Hobnob supports three secret backends via the `secrets.Provider` interface:

**LocalProvider (default)** — stores secrets as files on disk. Secret path `hobnob/db/dsn` maps to file `/srv/hobnob/secrets/hobnob/db/dsn`. Two modes:

- **Plaintext** (no `key_file`): files are read/written as-is. Suitable for dev, single-server, or when disk encryption provides at-rest protection.
- **Encrypted** (with `key_file`): files are AES-256-GCM encrypted with a master key.

```yaml
secrets:
  backend: local                                    # default when omitted
  # key_file: "/srv/hobnob/master.key"       # optional: enables encryption
  # secrets_dir: "/srv/hobnob/secrets"        # default
```

Supports secret versioning (file mtime) — the watcher detects changes and queues affected nodes for re-convergence. Manage secrets via CLI: `hobnobcli secrets set/get/list/delete`.

**IsopassProvider (production)** — retrieves secrets from an [isopass](https://github.com/dansheldon/isopass) instance over HTTP. Authenticates with a service account bearer token, exchanges it for a short-lived API token, and searches for secrets by path.

```yaml
secrets:
  backend: isopass
  url: https://localhost:8443
  token_file: /srv/hobnob/isopass-token  # file containing bearer token (0600)
  tls_skip_verify: true             # skip TLS cert verification (e.g. self-signed)
```

The server also polls isopass for secret version changes. When a secret rotates, all nodes referencing it are automatically queued for re-convergence.

### Security guarantees

- Secrets never appear in plaintext in config files, logs, gRPC payloads, or run results
- If the secrets provider is unreachable, the server fails fast — no fallback to insecure defaults
- Resolved secret values are wrapped in `SecretString`, which prints `[REDACTED]` on `String()`

## Server secrets

The server's own config (`server-configs/server.yaml`) uses `secret:` references resolved by the secrets backend. With the default `local` backend, secrets map to files in `/srv/hobnob/secrets/`. TLS certificates are stored separately in `/srv/hobnob/tls/` as plain PEM files:

| Path | Description |
|------|-------------|
| `secrets/hobnob/db/dsn` | Database connection string (encrypted) |
| `tls/ca` | CA certificate (PEM) |
| `tls/ca_key` | CA private key (PEM) |
| `tls/cert` | Server TLS certificate (PEM) |
| `tls/key` | Server TLS private key (PEM) |

TLS certificates are auto-generated on first start (local backend only).

## Development

```bash
make build          # Build server, agent, hobnobcli + hnbvi symlink
make proto          # Regenerate protobuf/gRPC code
make certs          # Generate self-signed TLS certs
make test           # Run all tests
make lint           # Run linter
make dist           # Cross-compile agent binaries
docker compose up   # Start server
```
