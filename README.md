# Isogov

Pull-based configuration management for Linux and macOS. Agents on managed nodes pull desired state from a central server via gRPC over mTLS, apply idempotent changes (files, packages, services, users), and report results back.

## Quickstart

### 1. Create `/etc/isogov`

All server config lives in `/etc/isogov` on the host. The container bind-mounts it read-only.

```bash
sudo mkdir -p /etc/isogov/certs
```

### 2. Generate TLS certificates

```bash
make certs
sudo cp certs/* /etc/isogov/certs/
```

For production, replace with your own CA and server cert.

### 3. Create the `.env` file

```bash
sudo tee /etc/isogov/.env <<'EOF'
ISOGOV_DB_DSN=postgres://isogov:secret@postgres:5432/isogov?sslmode=disable
ISOGOV_TLS_CERT=certs/server.crt
ISOGOV_TLS_KEY=certs/server.key
ISOGOV_TLS_CA=certs/ca.crt
EOF
```

### 4. Copy config files

```bash
sudo cp -r server-configs /etc/isogov/
sudo cp -r client-configs /etc/isogov/
```

### 5. Start the server

```bash
docker compose up -d
```

This starts PostgreSQL and the isogov server. Migrations run automatically. Verify:

```bash
docker compose ps
```

You should see `postgres` (healthy) and `server` (running). The dashboard is at `http://<server-ip>:8080/`.

### 6. Create an approved pattern and enrollment token

```bash
docker compose exec server gvctl tokens generate --pattern "*.example.com"
```

This prints a `curl` command with the enrollment token baked in.

### 7. Bootstrap a managed node

On each target node, run the `curl` command from the previous step:

```bash
curl http://<server-ip>:8080/bootstrap?token=<TOKEN> | bash
```

The bootstrap script detects OS/arch, downloads the agent binary, installs it, copies the CA cert, generates an ED25519 keypair, and enrolls with the server.

### 8. Define desired state

Edit `client-configs/clients.yaml` to map hostnames to modules and roles:

```yaml
# client-configs/clients.yaml
clients:
  - match: "web01.example.com"
    environment: production
    modules: [nginx]
    vars:
      doc_root: "/var/www/html"
```

Modules are defined in `client-configs/modules/`. For example, `client-configs/modules/nginx.yaml`:

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

The `source` field references a template file relative to `client-configs/` (e.g. `client-configs/templates/nginx.conf.tmpl`). You can also use inline `content` — the two are mutually exclusive. Templates use Go template syntax with `.Facts` and `.Vars` namespaces.

Group modules into roles in `client-configs/roles/`:

```yaml
# client-configs/roles/webserver.yaml
name: webserver
modules: [nginx, certbot]
```

The server watches `client-configs/` and hot-reloads automatically. On the next check-in, the agent converges to the desired state.

### 9. Verify convergence

```bash
# List enrolled clients
docker compose exec server gvctl clients list

# View run results
docker compose exec server gvctl runs list

# Check compliance
docker compose exec server gvctl clients show web01.example.com
```

Or open the web dashboard at `http://<server-ip>:8080/`.

## Config structure

```
client-configs/
├── clients.yaml          # Maps hostnames/globs to roles and modules
├── modules/              # Reusable resource sets with default vars
│   ├── nginx.yaml
│   ├── postgresql.yaml
│   └── ...
├── roles/                # Named groups of modules
│   ├── webserver.yaml
│   ├── database.yaml
│   └── ...
└── templates/            # External template files (referenced via source:)
    └── nginx.conf.tmpl
```

`clients.yaml` entries use glob patterns — first match wins. Client-level `vars` override module defaults. Clients matching no entry get an empty module list.

## Examples

Ready-to-use module definitions in `client-configs/modules/` and role groupings in `client-configs/roles/`:

| Module | File | What it configures |
|--------|------|--------------------|
| Nginx | [`client-configs/modules/nginx.yaml`](client-configs/modules/nginx.yaml) | Virtual host, worker tuning, `source:` template |
| PostgreSQL | [`client-configs/modules/postgresql.yaml`](client-configs/modules/postgresql.yaml) | `postgresql.conf` + `pg_hba.conf`, memory tuning |
| MySQL | [`client-configs/modules/mysql.yaml`](client-configs/modules/mysql.yaml) | InnoDB tuning, slow query log, security hardening |
| Redis | [`client-configs/modules/redis.yaml`](client-configs/modules/redis.yaml) | LRU eviction, AOF persistence, `secret:` password |
| Docker | [`client-configs/modules/docker.yaml`](client-configs/modules/docker.yaml) | `daemon.json`, log rotation, containerd |
| HAProxy | [`client-configs/modules/haproxy.yaml`](client-configs/modules/haproxy.yaml) | Stats page, roundrobin backend, health checks |
| OpenSSH + fail2ban | [`client-configs/modules/openssh.yaml`](client-configs/modules/openssh.yaml), [`fail2ban.yaml`](client-configs/modules/fail2ban.yaml) | Hardened sshd, key-only auth, brute-force protection |
| Prometheus + Grafana | [`client-configs/modules/prometheus.yaml`](client-configs/modules/prometheus.yaml), [`grafana.yaml`](client-configs/modules/grafana.yaml) | Prometheus, node-exporter, Grafana |
| CLI tools | [`client-configs/modules/charm-tools.yaml`](client-configs/modules/charm-tools.yaml), [`github-tools.yaml`](client-configs/modules/github-tools.yaml) | `repository` for Charm repo, `archive` for GitHub releases |

Client-to-module mappings are in [`client-configs/clients.yaml`](client-configs/clients.yaml), with roles like `webserver`, `database`, `monitoring`, and `bastion` in [`client-configs/roles/`](client-configs/roles/).

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
- **gvctl** — admin CLI, talks to server REST API
- **Config** — YAML files on disk, hot-reloaded on change

## Resource types

| Type | What it manages | Idempotency check |
|------|----------------|-------------------|
| `file` | File content, owner, group, mode | SHA256 hash comparison |
| `package` | Package installation/removal | Query package manager |
| `service` | Service state (running/stopped/restarted) | `systemctl` status |
| `user` | User accounts and attributes | `/etc/passwd` lookup |
| `repository` | Third-party package repos + GPG keys | Check repo config file exists |
| `archive` | Download + install binaries from URLs | SHA256 hash or `unless` command |
| `exec` | Arbitrary commands | `creates`/`onlyif`/`unless` guards |

## Key features

- **mTLS everywhere** — no plaintext gRPC connections
- **ED25519 auth** — every agent request is signature-verified
- **Dependency DAG** — resources declare `depends_on`, topologically sorted
- **Notify/subscribe** — config change triggers service restart
- **Conditional resources** — `when:` expressions evaluated against node facts
- **Fact collection** — OS, hardware, network, packages reported every check-in
- **Compliance tracking** — compliant/drifted/failing/stale/unknown per client
- **Secret rotation** — server polls isopass, force-runs affected clients
- **Webhooks** — HMAC-signed events for runs, drift, enrollment, rotation
- **Prometheus metrics** — `/metrics` endpoint with fleet-level gauges
- **Agent self-update** — automatic binary updates with checksum verification
- **Web dashboard** — fleet overview, client drill-down, compliance trends

## Secrets

Sensitive values in config files use the `secret:` prefix instead of plaintext. The server resolves these references at config load time before sending anything to agents — agents receive the resolved values, never the `secret:` references.

### Using secrets in client configs

Reference secrets by path in `client-configs/clients.yaml` or module definitions:

```yaml
# client-configs/clients.yaml
clients:
  - match: "cache01.example.com"
    modules: [redis]
    vars:
      redis_password: "secret:isogov/redis/password"
```

```yaml
# client-configs/modules/grafana.yaml (inline in resource attributes)
resources:
  - name: grafana-ini
    type: file
    targetpath: /etc/grafana/grafana.ini
    content: |
      admin_password = secret:isogov/grafana/admin-password
```

Certain field names (`dsn`, `tls_cert_path`, `tls_key_path`, `webhook_secret`) are enforced — the server rejects configs that set these to plaintext values.

### Secrets providers

Isogov supports two secret backends via the `secrets.Provider` interface:

**IsopassProvider (production)** — retrieves secrets from an [isopass](https://github.com/dansheldon/isopass) instance over HTTP. Authenticates with a service account bearer token, exchanges it for a short-lived API token, and searches for secrets by path. Configure with:

```bash
ISOPASS_URL=https://isopass.internal:8443
ISOPASS_BEARER_TOKEN=<service-account-token>
```

The server also polls isopass for secret version changes. When a secret rotates, all clients referencing it are automatically queued for re-convergence.

**StaticProvider (development)** — reads from a `.env` file. Secret paths are converted to environment variable names: `secret:isogov/db/dsn` → `ISOGOV_DB_DSN` (uppercase, slashes to underscores).

### Security guarantees

- Secrets never appear in plaintext in config files, logs, gRPC payloads, or run results
- If the secrets provider is unreachable, the server fails fast — no fallback to insecure defaults
- Resolved secret values are wrapped in `SecretString`, which prints `[REDACTED]` on `String()`

## Environment configuration

The server's own config (`server-configs/server.yaml`) also uses `secret:` references, resolved from `.env` via the StaticProvider:

| `.env` variable | Required | Description |
|-----------------|----------|-------------|
| `ISOGOV_DB_DSN` | Yes | PostgreSQL connection string |
| `ISOGOV_TLS_CERT` | Yes | Path to server TLS certificate |
| `ISOGOV_TLS_KEY` | Yes | Path to server TLS private key |
| `ISOGOV_TLS_CA` | Yes | Path to CA certificate |
| `ISOGOV_WEBHOOK_SECRET` | No | HMAC secret for webhook signing |
| `ISOGOV_VERSION` | No | Version string for agent self-update |
| `ISOGOV_AGENT_DIR` | No | Directory with cross-compiled agent binaries |

## Development

```bash
make build          # Build server, agent, and gvctl
make proto          # Regenerate protobuf/gRPC code
make certs          # Generate self-signed TLS certs
make test           # Run all tests
make lint           # Run linter
make dist           # Cross-compile agent binaries
make seed           # Insert dev seed data
docker compose up   # Start server + postgres
```
