# Vigo

Distributed state enforcement for servers, network devices, and workstations. Agents on managed nodes pull desired state from a central server via gRPC over mTLS, apply idempotent changes, and report results back.

Far more than configuration management. One server holds tens of thousands of nodes at a tight 1-second cadence — hundreds of thousands at a relaxed one — RAM-bound and linear at **~620 KB/node** with a realistic inventory (size per the [sizing guide](docs/howto/operate/sizing.md)), with peer-equal `spanner` federation beyond a single host's ceiling. Around the convergence engine (69 resource types, 50 trait collectors, 7 operating systems — Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos, Windows) Vigo bundles regulatory-framework compliance scoring + GRC export, Connwaer active verification, browser SSH/RDP/VNC remote access with no inbound ports (Scrier), a peer-to-peer content/git/artifact substrate, orchestration (ad-hoc tasks, workflows, rolling execution, live queries), a read-only AI assistant, RBAC with pluggable authentication (basic, OIDC, SSH key signing), a tamper-evident audit log, and a web dashboard.

Open source under the [Apache License 2.0](LICENSE): every feature, no node limits, no paid tiers.

## Quickstart

### 1. Build and start the server

Prebuilt images are not published yet, so build from source. You need Docker, Go 1.25 and a Rust toolchain.

```bash
git clone https://github.com/daniel-alexander4/vigo.git && cd vigo
sudo scripts/upgrade.sh full --norun   # builds the server image and the amd64 agent
IMAGE=us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest   # the local image tag
sudo mkdir -p /srv/vigo
docker run --rm -v /srv/vigo:/srv/vigo "$IMAGE" --seed-only
cd /srv/vigo && docker compose up -d
```

`--seed-only` fills `/srv/vigo/` with `docker-compose.yml`, `server.yaml`, `.env`, TLS certificates, and example configs; `docker compose up -d` starts the server. Dashboard at `https://localhost:8443/`. Then enroll the server's own agent over loopback: `curl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh`.

### 2. Bootstrap managed nodes

On each node you want to manage:

```bash
# Linux / macOS / FreeBSD / OpenBSD / NetBSD / illumos
curl -sSfk https://<server-ip>:8443/bootstrap | sudo sh

# Windows (PowerShell as Administrator)
irm https://<server-ip>:8443/bootstrap?os=windows | iex
```

The server generates a script on the fly that downloads the agent binary for the node's OS/arch, enrolls it via CSR signing, and starts it as a service (systemd, launchd, rc.d, rcctl, SMF, or Windows Service). No package manager needed on the node.

### 3. Define desired state

Map hostnames to configcrates and roles in `.vgo` files (YAML syntax):

```yaml
# stacks/envoys.vgo
envoys:
  - match: "web01.example.com"
    environment: production
    roles: [webserver]
    vars:
      doc_root: "/var/www/html"

  - match: "*.db.internal"
    environment: production
    roles: [database]
    vars:
      pg_max_connections: "200"

  - match: "monitoring.example.com"
    configcrates: [prometheus, grafana, node-exporter]
```

### 4. Publish and verify

```bash
vigocli config publish
vigocli envoys list
vigocli runs list
```

Or open the web dashboard at `https://<server-ip>:8443/`.

## Examples

### Nginx with templated config

```yaml
# stacks/configcrates/nginx.vgo
name: nginx
vars:
  worker_processes: "auto"
  server_name: "example.com"
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present

  - name: nginx-conf
    type: file
    target_path: /etc/nginx/nginx.conf
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

### PostgreSQL with secrets

```yaml
# stacks/configcrates/postgresql.vgo
name: postgresql
vars:
  pg_password: "secret:vigo/postgres/password"
  shared_buffers: "256MB"
  max_connections: "100"
resources:
  - name: install-postgres
    type: package
    package: postgresql
    state: present

  - name: pg-conf
    type: file
    target_path: /etc/postgresql/16/main/postgresql.conf
    content: |
      shared_buffers = {{ .Vars.shared_buffers }}
      max_connections = {{ .Vars.max_connections }}
      listen_addresses = '*'
      ssl = on
    owner: postgres
    mode: "0600"
    depends_on: [install-postgres]
    notify: [pg-service]

  - name: pg-hba
    type: file
    target_path: /etc/postgresql/16/main/pg_hba.conf
    content: |
      local   all   postgres   peer
      host    all   all        0.0.0.0/0   scram-sha-256
    owner: postgres
    mode: "0600"
    notify: [pg-service]

  - name: pg-service
    type: service
    service: postgresql
    enabled: true
    state: running
    subscribes: [pg-conf, pg-hba]
```

### Docker container with resource wiring

```yaml
# stacks/configcrates/redis.vgo
name: redis
vars:
  redis_password: "secret:vigo/redis/password"
  maxmemory: "256mb"
resources:
  - name: redis-conf
    type: file
    target_path: /etc/redis/redis.conf
    content: |
      bind 0.0.0.0
      requirepass {{ .Vars.redis_password }}
      maxmemory {{ .Vars.maxmemory }}
      maxmemory-policy allkeys-lru
      appendonly yes
    owner: redis
    mode: "0600"
    notify: [redis-container]

  - name: redis-container
    type: container
    container: redis
    image: redis:7-alpine
    state: running
    subscribes: [redis-conf]
```

### OS-conditional packages

```yaml
# stacks/configcrates/webserver.vgo
name: webserver
vars:
  is_debian:
    trait: os.distro
    only: [debian, ubuntu]
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
    when: "!is_debian"

  - name: nginx-service
    type: service
    service: nginx
    enabled: true
    state: running
```

### User accounts with SSH keys

```yaml
# stacks/configcrates/ops-users.vgo
name: ops-users
resources:
  - name: deploy-user
    type: user
    username: deploy
    shell: /bin/bash
    groups: [sudo, docker]
    state: present

  - name: deploy-ssh-key
    type: sshkey
    hostname: deploy.example.com
    sshkey_type: ssh-ed25519
    sshkey: "AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyHere"
    state: present
    depends_on: [deploy-user]
```

### Exec with idempotency guards

```yaml
# stacks/configcrates/certbot.vgo
name: certbot
vars:
  domain: "example.com"
resources:
  - name: install-certbot
    type: package
    package: certbot
    state: present

  - name: obtain-cert
    type: exec
    command: "certbot certonly --nginx -d {{ .Vars.domain }} --non-interactive --agree-tos -m admin@{{ .Vars.domain }}"
    unless: "test -f /etc/letsencrypt/live/{{ .Vars.domain }}/fullchain.pem"
    depends_on: [install-certbot]
```

### Firewall rules

```yaml
# stacks/configcrates/firewall.vgo
name: firewall
resources:
  - name: allow-ssh
    type: firewall
    port: "22"
    protocol: tcp
    action: accept
    state: present

  - name: allow-http
    type: firewall
    port: "80"
    protocol: tcp
    action: accept
    state: present

  - name: allow-https
    type: firewall
    port: "443"
    protocol: tcp
    action: accept
    state: present
```

### Cron job

```yaml
# stacks/configcrates/backups.vgo
name: backups
resources:
  - name: backup-script
    type: file
    target_path: /usr/local/bin/backup.sh
    content: |
      #!/bin/bash
      pg_dump mydb | gzip > /var/backups/mydb-$(date +%Y%m%d).sql.gz
      find /var/backups -name "mydb-*.sql.gz" -mtime +7 -delete
    owner: root
    mode: "0755"

  - name: nightly-backup
    type: cron
    user: postgres
    schedule: "0 2 * * *"
    command: /usr/local/bin/backup.sh
    depends_on: [backup-script]
```

### Network device configuration

```yaml
# stacks/configcrates/switch-baseline.vgo
name: switch-baseline
resources:
  - name: set-ntp
    type: network_ntp
    servers: ["10.0.0.1", "10.0.0.2"]
    state: present

  - name: set-syslog
    type: network_syslog
    host: "10.0.0.5"
    port: "514"
    state: present

  - name: login-banner
    type: network_banner
    banner_type: motd
    text: "Authorized access only. All activity is monitored."
    state: present
```

### Roles and match blocks

```yaml
# stacks/roles.vgo
roles:
  - name: webserver
    configcrates: [nginx, certbot, firewall, ops-users, node-exporter]

  - name: database
    configcrates: [postgresql, backups, firewall, ops-users, node-exporter]
```

```yaml
# stacks/envoys.vgo
envoys:
  - match: "web*.example.com"
    environment: production
    roles: [webserver]
    vars:
      server_name: "example.com"

  - match: "db*.example.com"
    environment: production
    roles: [database]
    vars:
      shared_buffers: "1GB"
      max_connections: "400"

  - match: "staging-*"
    environment: staging
    roles: [webserver, database]
    environment_overrides:
      staging:
        shared_buffers: "128MB"
```

## Architecture

```
┌──────────────┐     gRPC/mTLS     ┌──────────────┐
│   Agent       │◄─────────────────►│   Server      │
│  (per node)   │     pull loop     │  :1530 gRPC   │
│               │                   │  :8443 REST   │
└──────────────┘                   └──────┬───────┘
                                          │
                            ┌─────────────┼─────────────┐
                            │             │             │
                       ┌────▼───┐   ┌─────▼────┐  ┌────▼───┐
                       │ SQLite │   │  Config   │  │Secrets │
                       │  (WAL) │   │  (YAML)   │  │backend │
                       └────────┘   └──────────┘  └────────┘
```

- **vigosrv** — Go server: gRPC (port 1530) + REST API + web UI + metrics (port 8443)
- **vigo** — Rust agent daemon on each managed node
- **vigocli** — Go admin CLI, talks to server REST API

## Resource types

69 resource types — 68 built-in plus the user-supplied `custom` executor — across 7 operating systems (the table lists them with their per-OS implementations):

| Category | Types |
|----------|-------|
| **Files & content** | `file`, `directory`, `symlink`, `file_line`, `blockinfile`, `ini`, `json_file`, `replace`, `field_edit`, `logrotate` |
| **Packages** | `package`, `repository`, `pip`, `gem`, `npm`, `archive`, `git`, `alternatives` |
| **Services** | `service`, `systemd_dropin` |
| **Users & groups** | `user`, `group`, `sshkey` |
| **System** | `exec`, `cron`, `hostname`, `timezone`, `sysctl`, `mount`, `swap`, `tmpfile`, `host`, `kernel_module`, `firewall`, `selinux`, `reboot`, `process` |
| **Containers** | `container` |
| **Network devices** | `ssh_exec`, `network_config`, `cisco_vlan`, `cisco_interface`, `cisco_acl`, `junos_interface`, `network_ntp`, `network_syslog`, `network_snmp`, `network_user`, `network_banner`, `network_interface` |
| **macOS** | `service_macos`, `user_macos`, `group_macos`, `hostname_macos`, `timezone_macos` |
| **BSD** | `firewall_pf`, `hostname_bsd`, `mount_bsd`, `swap_bsd`, `sysctl_bsd`, `timezone_bsd`, `service_freebsd`, `user_freebsd`, `group_freebsd`, `kernel_module_freebsd`, `service_openbsd` |
| **illumos** | `service_illumos`, `timezone_illumos` |
| **Windows** | `service_windows`, `package_windows`, `user_windows`, `firewall_windows`, `cron_windows`, `hostname_windows`, `timezone_windows`, `registry`, `windows_env`, `reboot_windows`, `powershell`, `certificate_windows`, `domain_membership`, `local_security_policy`, `service_recovery_windows`, `windows_update`, `windows_defender` |

All executors are idempotent — they check current state before acting. `exec` requires an idempotency guard (`onlyif`, `unless`, or `subscribes`).

## Orchestration

### Ad-hoc tasks

Dispatch commands to targeted nodes in real-time:

```bash
vigocli task run restart-nginx --target "*.web.example.com"
vigocli task status <run-id>
```

### Workflows

Chain tasks with conditional branching:

```yaml
# stacks/workflows/deploy.vgo
name: rolling-deploy
steps:
  - task: pre-check
    target: "*.web.example.com"
  - task: deploy-code
    target: "*.web.example.com"
    rolling:
      batch_size: 25%
      health_check: "curl -sf http://localhost/health"
  - task: post-check
    target: "*.web.example.com"
```

### Live queries

Query fleet state in real-time:

```bash
vigocli query run "df -h /var" --target "*.db.example.com"
```

## Authentication

Three pluggable methods:

- **basic** (default) — username/password, bcrypt hashes in secrets store
- **oidc** — OpenID Connect SSO
- **isowebauth** — SSH key signing via desktop app

API tokens (`vgot_` prefix) for programmatic access. Three roles: admin, viewer, and compliance. SCS sessions backed by SQLite.

## Secrets

Config values use the `secret:` prefix — resolved server-side before anything reaches agents:

```yaml
vars:
  db_password: "secret:vigo/db/password"
```

Two backends: **local** (default, AES-256-GCM encrypted files on disk) and **isopass** (production, HTTP-based). Automatic re-convergence on secret rotation. Secrets never appear in plaintext in configs, logs, gRPC payloads, or run results.

## Spanner

Peer-equal federation for fleets beyond one server. Each bolt is a write-equal peer owning a hostname-pattern partition; every bolt holds the full admissions roster, replicated over gossip, so admin reads, enrollment, force-push, reassign and drain work from any bolt. No hub. Configured under `spanner:` in `server.yaml`; driven by `vigocli spanner`.

## License

[Apache License 2.0](LICENSE). Copyright (c) 2026 Alexander4, LLC.
Contributions are welcome under the same license with a DCO sign-off — see
[CONTRIBUTING.md](CONTRIBUTING.md). The Vigo name and logo are trademarks; see
[TRADEMARKS.md](TRADEMARKS.md). Questions and bug reports: open an issue, or
email vigo.support@alexander4.io.
