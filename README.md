# Isogov

Pull-based configuration management for Linux and macOS. Agents on managed nodes pull desired state from a central server via gRPC over mTLS, apply idempotent changes (files, packages, services, users), and report results back.

## Quickstart

### 1. Create `/etc/isogov`

All server config lives in `/etc/isogov` on the host. The container bind-mounts it read-only.

```bash
sudo mkdir -p /etc/isogov/{config/clients,certs}
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
sudo cp config/server.yaml /etc/isogov/config/
sudo cp config/classification.yaml /etc/isogov/config/
sudo cp config/clients/*.yaml /etc/isogov/config/clients/
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

Create a client config in `/etc/isogov/config/clients/`:

```yaml
# /etc/isogov/config/clients/web01.example.com.yaml
client: web01.example.com
environment: production

vars:
  doc_root: "/var/www/html"

modules:
  - name: webserver
    resources:
      - name: install-nginx
        type: package
        package: nginx
        state: present

      - name: nginx-config
        type: file
        path: /etc/nginx/sites-available/default
        content: |
          server {
              listen 80;
              root {{ .Vars.doc_root }};
          }
        owner: root
        group: root
        mode: "0644"
        depends_on: [install-nginx]
        notify: [restart-nginx]

      - name: restart-nginx
        type: service
        service: nginx
        enabled: true
        state: running
        subscribes: [nginx-config]
```

The server watches `/etc/isogov/config/clients/` and hot-reloads automatically. On the next check-in, the agent converges to this state.

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

## Auto-classification

Instead of per-host configs, define patterns in `config/classification.yaml`:

```yaml
rules:
  - match: "web-*"
    environment: production
    modules:
      - name: webserver
        resources:
          - name: install-nginx
            type: package
            package: nginx
            state: present

  - match: "db-*"
    environment: production
    modules:
      - name: database
        resources:
          - name: install-postgres
            type: package
            package: postgresql
            state: present
```

First match wins. Explicit client YAML always takes precedence over classification rules. Clients matching no rule get an empty module list.

## Examples

Ready-to-use configs for popular Linux software in `config/clients/` and `config/classification.yaml`:

| Example | File | What it configures |
|---------|------|--------------------|
| Nginx | [`config/clients/webserver.yaml`](config/clients/webserver.yaml) | Virtual host, worker tuning, per-env overrides |
| PostgreSQL | [`config/clients/database.yaml`](config/clients/database.yaml) | `postgresql.conf` + `pg_hba.conf`, memory tuning |
| MySQL | [`config/clients/mysql.yaml`](config/clients/mysql.yaml) | InnoDB tuning, slow query log, security hardening |
| Redis | [`config/clients/redis.yaml`](config/clients/redis.yaml) | LRU eviction, AOF persistence, `secret:` password |
| Docker | [`config/clients/docker-host.yaml`](config/clients/docker-host.yaml) | `daemon.json`, log rotation, containerd |
| HAProxy | [`config/clients/haproxy.yaml`](config/clients/haproxy.yaml) | Stats page, roundrobin backend, health checks |
| OpenSSH + fail2ban | [`config/clients/openssh.yaml`](config/clients/openssh.yaml) | Hardened sshd, key-only auth, brute-force protection |
| Prometheus + Grafana | [`config/clients/monitoring.yaml`](config/clients/monitoring.yaml) | Prometheus, node-exporter, Grafana (`repository` for APT repo) |
| CLI tools | [`config/clients/cli-tools.yaml`](config/clients/cli-tools.yaml) | `repository` for Charm repo, `archive` for GitHub releases (bat, fd, rg) |

The [`config/classification.yaml`](config/classification.yaml) file includes matching rules for all of these roles (`web-*`, `db-*`, `mysql-*`, `cache-*`, `lb-*`, `docker-*`, `mon-*`, `bastion-*`).

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

## Environment configuration

All `secret:` references in `config/server.yaml` are resolved from `.env`:

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
