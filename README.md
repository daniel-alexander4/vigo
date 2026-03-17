# Hobnob

Distributed state enforcement for servers, network devices, and workstations. Agents on managed nodes pull desired state from a central server via gRPC over mTLS, apply idempotent changes, and report results back.

73 built-in resource types. 27 trait collectors. 7 operating systems (Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos, Windows). Orchestration (ad-hoc pushes, workflows, rolling execution, live queries). Pluggable authentication (basic, OIDC, SSH key signing). Hub-spoke scaling via spanner. AI assistant. Web dashboard.

## Quickstart

### 1. Start the server

```bash
sudo mkdir -p /srv/hobnob
docker run --rm -v /srv/hobnob:/srv/hobnob ghcr.io/daniel-alexander4/hobnob:latest --seed-only
cd /srv/hobnob
docker compose up -d
```

The first command seeds `/srv/hobnob/` with `docker-compose.yml`, `server.yaml`, `.env`, TLS certificates, and example configs. The second starts the server. Dashboard at `https://localhost:8443/`.

### 2. Bootstrap managed nodes

On each node you want to manage:

```bash
# Linux / macOS / FreeBSD / OpenBSD / NetBSD / illumos
curl -sSfk https://<server-ip>:8443/bootstrap | sudo sh

# Windows (PowerShell as Administrator)
irm https://<server-ip>:8443/bootstrap?os=windows | iex
```

The server generates a script on the fly that downloads the agent binary for the node's OS/arch, enrolls it via CSR signing, and starts it as a service (systemd, launchd, rc.d, rcctl, SMF, or Windows Service). No package manager needed on the node.

#### What bootstrap does

1. Downloads the pre-built agent binary from the server
2. Generates an ED25519 keypair + ECDSA TLS keypair
3. Sends a CSR to the server
4. Server validates the node (trusted pattern + CIDR, or one-time token)
5. Server signs the CSR and returns the signed TLS cert + CA cert
6. Agent saves certs, keypair, and node UUID
7. Creates and starts the agent service

After bootstrap, the agent checks in via mTLS every 5 minutes.

#### Enrollment modes

**Trusted enrollment (default)** — auto-enrolls nodes matching hostname patterns and CIDR ranges:

```yaml
bootstrap:
  cert_validity: "8760h"
  trusted_enrollment:
    - pattern: "*.web.example.com"
      cidrs: ["10.0.1.0/24"]
    - pattern: "*.db.internal"
      cidrs: ["10.0.2.0/24"]
```

**Token-based enrollment** — for untrusted networks or explicit per-node approval:

```bash
hobnobcli tokens generate
hobnobcli approved add "web-01.example.com" --token <TOKEN>
# On the node:
curl -sSf "https://<server-ip>:8443/bootstrap?token=<TOKEN>" | sudo sh
```

### 3. Define desired state

Map hostnames to modules and roles in `.hnb` files (YAML syntax):

```yaml
# thrall-configs/nodes.hnb
nodes:
  - match: "web01.example.com"
    environment: production
    modules: [nginx]
    vars:
      doc_root: "/var/www/html"
```

Modules define resources:

```yaml
# thrall-configs/modules/nginx.hnb
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

Group modules into roles:

```yaml
# thrall-configs/roles/webserver.hnb
name: webserver
modules: [nginx, certbot]
```

Publish and reload:

```bash
hobnobcli config publish
```

### 4. Verify convergence

```bash
hobnobcli thralls list
hobnobcli runs list
hobnobcli thralls show web01.example.com
```

Or open the web dashboard at `https://<server-ip>:8443/`.

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

Four binaries:

- **hobd** — Go server: gRPC (port 1530) + REST API + web UI + metrics (port 8443)
- **hobnob** — Rust agent daemon on each managed node
- **hobnobcli** — Go admin CLI, talks to server REST API
- **hobnob-site** — Go public website

**Port 1530** — approximate first recorded written use of "hobgoblin" in English.

## Resource types

73 built-in executors across 7 operating systems:

| Category | Types |
|----------|-------|
| **Files & content** | `file`, `directory`, `symlink`, `file_line`, `blockinfile`, `ini`, `json_file`, `replace`, `field_edit`, `logrotate` |
| **Packages** | `package`, `repository`, `pip`, `gem`, `npm`, `archive`, `git`, `alternatives` |
| **Services** | `service`, `systemd_dropin` |
| **Users & groups** | `user`, `group`, `sshkey` |
| **System** | `exec`, `cron`, `hostname`, `timezone`, `sysctl`, `mount`, `swap`, `tmpfile`, `host`, `kernel_module`, `firewall`, `selinux`, `reboot`, `process` |
| **Containers** | `docker_container`, `docker_image`, `docker_compose` |
| **Network devices** | `ssh_exec`, `network_config`, `cisco_vlan`, `cisco_interface`, `cisco_acl`, `junos_interface`, `network_ntp`, `network_syslog`, `network_snmp`, `network_user`, `network_banner`, `network_interface` |
| **macOS** | `service_macos`, `user_macos`, `group_macos`, `hostname_macos`, `timezone_macos` |
| **BSD** | `firewall_pf`, `hostname_bsd`, `mount_bsd`, `swap_bsd`, `sysctl_bsd`, `timezone_bsd`, `service_freebsd`, `user_freebsd`, `group_freebsd`, `kernel_module_freebsd`, `service_openbsd` |
| **illumos** | `service_illumos`, `timezone_illumos` |
| **Windows** | `service_windows`, `package_windows`, `user_windows`, `firewall_windows`, `cron_windows`, `hostname_windows`, `timezone_windows`, `registry`, `acl`, `windows_feature`, `windows_env`, `reboot_windows`, `powershell`, `iis_site`, `iis_app_pool`, `certificate_windows`, `domain_membership`, `local_security_policy`, `service_recovery_windows`, `windows_update`, `windows_defender`, `smb_share` |

All executors are idempotent — they check current state before acting. `exec` requires an idempotency guard (`creates`, `onlyif`, `unless`, or `subscribes`).

## Trait collection

27 trait collectors report facts about each node on every check-in: OS, hardware, network, packages, time, identity, virtualization, uptime, load, mountpoints, processors, DMI, cloud, filesystem, kernel, users, memory, block devices, Docker, systemd, SELinux, timezone, locale, swap, GPU, certificate expiry, and listening ports.

Platform-specific collectors extend this: 25 for BSD/macOS, 9 for illumos, 13 for Windows, 1 for network devices.

Traits power conditional resources (`when:` expressions), template rendering (`.Traits` namespace), and fleet inventory queries.

## Conditionals

Resources use `when:` expressions to control where they run, evaluated server-side:

```yaml
resources:
  - name: install-nginx
    type: package
    package: nginx
    when: "os_family('debian') && has_enough_ram"

  - name: install-httpd
    type: package
    package: httpd
    when: "os_family('rhel')"
```

Supports `&&`, `||`, `!`, parentheses, builtin functions (`os_family()`, `is_container`, `file_exists()`, etc.), and conditional vars defined against traits. Whole modules can also be conditional.

## Orchestration

### Ad-hoc pushes

Dispatch commands to targeted nodes in real-time:

```bash
hobnobcli push run restart-nginx --target "*.web.example.com"
hobnobcli push status <run-id>
```

### Workflows

Chain tasks with conditional branching:

```yaml
# tasks/deploy.yaml
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
hobnobcli query run "df -h /var" --target "*.db.example.com"
```

### Rolling execution

Batch-based execution with health checks between rounds. Configurable batch size, pause duration, and failure thresholds.

## Authentication

Three pluggable methods:

- **basic** (default) — username/password, bcrypt hashes in secrets store
- **oidc** — OpenID Connect SSO
- **isowebauth** — SSH key signing via desktop app

API tokens (`hbt_` prefix) for programmatic access. Two roles: admin and viewer. SCS sessions backed by SQLite.

## Secrets

Config values use the `secret:` prefix — resolved server-side before anything reaches agents:

```yaml
vars:
  db_password: "secret:hobnob/db/password"
```

Two backends: **local** (default, AES-256-GCM encrypted files on disk) and **isopass** (production, HTTP-based). Automatic re-convergence on secret rotation. Secrets never appear in plaintext in configs, logs, gRPC payloads, or run results.

## Spanner

Hub-spoke scaling for large fleets. A hub aggregates data from spokes, each handling a partition of nodes. Per-spoke compliance metrics, drill-down views, and thrall reassignment with drain support.

## Config structure

```
thrall-configs/
├── nodes.hnb             # Maps hostnames/globs to roles and modules
├── modules/              # Reusable resource sets with default vars
│   ├── nginx.hnb
│   └── ...
├── roles/                # Named groups of modules
│   ├── webserver.hnb
│   └── ...
└── templates/            # External template files (referenced via source:)
    └── nginx.conf.tmpl
```

Config files use `.hnb` extension (YAML syntax). First match wins in `nodes.hnb`. Node-level vars override module defaults.

## CLI

23 subcommands:

```
approved    thralls     runs        push        query       vi
inventory   license     doctor      spanner     ask         workflow
secrets     export      auth        users       webhooks    tokens
config      backup      db          test        server
```

## Web dashboard

HTMX-based UI at `https://<server>:8443/`:

- Fleet overview with compliance sparklines
- Node drill-down (traits, run history, compliance status)
- Push and workflow management with live status
- Live query results
- Config publish history and audit trail
- Spanner spoke drill-down
- User and token management (admin)
- Built-in documentation browser at `/docs`
- AI assistant drawer (Claude, OpenAI, or Ollama)

## Development

```bash
make build          # Build all binaries (hobd, hobnobcli, hobnob agent)
make build-agent    # Build Rust agent only
make proto          # Regenerate protobuf/gRPC code
make sqlc           # Regenerate type-safe Go from SQL queries
make test           # Run all tests (Go + Rust, 1500+)
make lint           # golangci-lint + buf lint + cargo clippy
make dist           # Cross-compile server/CLI static binaries
make dist-agent     # Cross-compile Rust agent (musl, amd64+arm64)
```

## License

Proprietary. Copyright (c) 2026 Alexander4, LLC.
