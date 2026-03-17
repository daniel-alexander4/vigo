# Hobnob Fact Sheet

## What It Is

Hobnob is a distributed state enforcement engine. It ensures every machine in your fleet is configured exactly how you declared it, continuously and automatically. You describe desired state in YAML. Hobnob makes it so.

## Architecture

Three binaries. Nothing else.

| Component | Language | What it does |
|-----------|----------|-------------|
| `hobd` | Go | Central server. gRPC + REST + Web UI. One process. |
| `hobnob` | Rust | Agent on each managed machine. One process, ~5 MB static binary. |
| `hobnobcli` | Go | Admin CLI. Talks to the server's REST API. |

No external database. No message broker. No JVM. No language runtime on managed nodes. No infrastructure to manage beyond the server itself.

**The server runs on SQLite.** WAL mode, single file, zero administration. The in-memory FleetIndex handles all check-in operations with zero database queries on the hot path.

**One server handles your entire fleet.** The architecture was designed so that a single small server can manage hundreds of thousands of nodes. There is no need for compile masters, worker pools, or database clusters regardless of fleet size.

## Configuration

One format, one path, one way to configure:

```yaml
# /srv/hobnob/hnb/proposed/modules/webserver.hnb
name: webserver
resources:
  - name: Install nginx
    type: package
    package: nginx

  - name: nginx config
    type: file
    targetpath: /etc/nginx/nginx.conf
    content: |
      worker_processes {{ .Traits.hardware.cpu_count }};
    notify: restart-nginx

  - name: restart-nginx
    type: service
    service: nginx
    state: restarted
    subscribes: nginx config
```

A sysadmin unfamiliar with Hobnob can read this and understand what it does. That is a design goal, not an accident.

- **YAML only.** No custom DSL, no embedded programming language, no compile step.
- **Go templates** in `content:` fields for dynamic values. Traits and variables are the only two namespaces.
- **First-match-wins** pattern matching for node assignments. No inheritance chains, no precedence rules to debug.
- **Single config path.** Edit files, publish, done. No API writes, no database mutations, no UI config changes.

## Security

Every connection is authenticated and encrypted. There are no optional security features — security is the default and only mode.

- **mTLS on all gRPC traffic.** Agent-to-server communication is mutual TLS with no plaintext fallback.
- **ED25519 request signing.** Every agent request is signature-verified against stored public keys. Replay and impersonation are not possible.
- **One-time enrollment tokens.** Bcrypt-hashed at rest, pattern-matched against approved hostnames. Used once, then invalidated.
- **Secrets never in plaintext.** The `secret:` prefix in YAML resolves through a pluggable secrets provider at config load time. Secrets never appear in config files, environment variables, logs, database rows, gRPC payloads, or run results.
- **Revocation is immediate.** A revoked agent gets an error on its next check-in and cannot re-enroll.
- **Source-available agent.** The code that runs as root on your machines is readable and auditable.

## Install the Server

One command on any Linux host with Docker:

```bash
docker run --rm -v /srv/hobnob:/srv/hobnob ghcr.io/daniel-alexander4/hobnob:latest --seed-only && cd /srv/hobnob && docker compose up -d
```

This seeds the data directory (config, TLS certs, database, examples), then starts the server. Ready to enroll machines immediately.

## Enroll a Machine

One command on each managed machine:

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

Downloads the agent binary, generates TLS certificates via CSR signing, registers with the server, installs the platform-appropriate service, and verifies connectivity. A machine is under management in seconds.

Works across all supported platforms: Linux (systemd), macOS (launchd), FreeBSD/NetBSD (rc.d), OpenBSD (rcctl), illumos (SMF), and Windows (PowerShell: `irm https://<server>:8443/bootstrap?os=windows | iex`).

## Platform Support

One agent binary, seven operating system families, 98 built-in executors.

| Platform | Init System | Package Manager | Fully Supported |
|----------|-------------|-----------------|:---------------:|
| Linux | systemd | apt, dnf, yum | Yes |
| macOS | launchd | Homebrew | Yes |
| FreeBSD | rc.d | pkg | Yes |
| OpenBSD | rcctl | pkg_add | Yes |
| NetBSD | rc.d | pkgin | Yes |
| illumos | SMF | IPS | Yes |
| Windows | Windows Service | Chocolatey, winget, Scoop | Yes |

The same `.hnb` config works across all platforms. Write `type: service` and the agent dispatches to the correct backend — systemd on Linux, launchctl on macOS, rcctl on OpenBSD, sc.exe on Windows. No platform-specific configs required.

Network devices (Cisco IOS/NX-OS/IOS-XR, Arista EOS, Juniper Junos) are managed via gateway proxy over SSH with 16 dedicated executors.

## Executors

98 built-in resource types, organized by scope:

**System:** package, service, user, group, cron, hostname, timezone, sysctl, firewall, kernel_module, mount, swap, reboot, selinux, alternatives, tmpfile, logrotate, systemd_dropin

**Files:** file, directory, symlink, file_line, blockinfile, replace, field_edit, ini, json_file, archive, repository, git

**Containers:** docker_compose, docker_container, docker_image

**Commands:** exec, process, ssh_exec, custom (JSON protocol for external scripts)

**Language packages:** pip, gem, npm

**Windows (22):** service, package, user, firewall, cron, hostname, timezone, registry, acl, windows_feature, windows_env, reboot, powershell, iis_site, iis_app_pool, certificate, domain_membership, local_security_policy, windows_update, windows_defender, smb_share, service_recovery

**Network devices (16):** ssh_exec, network_config, network_interface, network_vlan, network_acl, network_static_route, network_dns, network_ntp, network_syslog, network_snmp, network_user, network_banner, cisco_vlan, cisco_interface, cisco_acl, junos_interface

Every executor is idempotent: check current state, compare against desired state, act only on drift.

## Traits

27 built-in trait collectors (with platform-specific implementations across all 7 OS families) discover facts about each machine automatically: OS, hardware, CPU, memory, network, disks, filesystems, packages, uptime, load, virtualization, cloud provider, Docker, SELinux, locale, GPU, certificates, listening ports, and more. Custom trait collectors can be added via external scripts.

Traits are available in templates (`{{ .Traits.os.distro }}`) and conditional expressions (`when: "os_family('debian')"`).

## Resilience

The agent is designed to survive anything short of hardware failure:

- **Offline convergence.** When the server is unreachable, the agent continues applying its last-known policy from the local LMDB state store. Results are queued and drained when connectivity returns.
- **Compiled policy bundles.** The server signs bundles with ED25519. The agent verifies and caches them locally. Bundle TTL with proactive renewal at 80%.
- **Exponential backoff.** Server unavailability triggers backoff, not crashes. The agent never exits on transient failures.
- **Adaptive streaming.** Agents default to lightweight stateless polling. Persistent streams are promoted on demand only when the server has work to dispatch, then released when idle. This eliminates ~230 KiB/connection overhead for idle agents.

## Orchestration

Beyond continuous convergence, Hobnob supports ad-hoc operations:

- **Tasks.** Push commands to targeted agents in real time. Results stream back as they complete.
- **Workflows.** Chain tasks into multi-step sequences with conditional branching and abort support.
- **Rolling execution.** Split targets into batches with health checks between rounds. Configurable batch size, failure thresholds, and inter-batch delays.
- **Live queries.** Run read-only commands across the fleet and aggregate results instantly.
- **Reusable task definitions.** Store common operations in YAML files. Execute by name with parameter overrides.

## Spanner

Hub-spoke topology for multi-site and multi-region deployments:

- A hub server routes enrollment to the nearest spoke based on hostname patterns.
- Admin queries fan out from the hub to all spokes and aggregate results.
- Per-spoke compliance breakdown and latency metrics.
- Thrall reassignment between spokes with zero re-enrollment. Spoke drain for maintenance.

## Observability

- **Web UI.** HTMX-based dashboards for fleet overview, compliance, runs, tasks, workflows, queries, spanner, and license status. No JavaScript framework, no build step.
- **Prometheus metrics.** 20+ gauges, counters, and histograms covering fleet health, gRPC performance, compliance, license, streams, and AI usage.
- **Compliance monitoring.** Every node is continuously classified as compliant, drifted, failing, stale, or unknown. Sparkline history, threshold alerts, compliance export (SIEM, CMDB, OSCAL).
- **Webhooks.** HMAC-SHA256 signed event delivery with retry and backoff.
- **Email notifications.** Stale nodes, compliance thresholds, secret rotation, config reload failures.
- **Audit log.** Config changes, enrollments, revocations, task executions — all logged with attribution.

## AI Assistant

Built-in AI assistant accessible from every page of the web UI:

- Supports Ollama (local), Claude, and OpenAI backends.
- 14 tools for fleet queries, node inspection, compliance checks, config exploration, and documentation search.
- Documentation corpus injection for context-aware answers about your specific Hobnob deployment.

## Operations

- **Config validation and preview.** Validate configs before publishing. Preview what would change for any node without applying.
- **Config rollback.** Revert to any previous published config with one command.
- **Backup and restore.** Point-in-time server snapshots via CLI. Optional Litestream WAL replication for continuous backup.
- **Live reload.** `config publish` validates, syncs, and reloads the server in one command. No restart required.
- **License management.** Built-in free tier (25 nodes). Gradual enforcement: 15-day grace, 15-day enrollment block, then service stop. Existing nodes are never locked out.

## Numbers

| Metric | Value |
|--------|-------|
| Server binary | Single Go binary |
| Agent binary | ~5 MB static (Linux amd64) |
| Agent memory | ~8 MB RSS idle |
| Built-in executors | 98 |
| Trait collectors | 28 (with platform variants across 7 OS families) |
| Supported platforms | 7 OS families + 5 network device types |
| Database | SQLite (embedded, zero-admin) or PostgreSQL |
| Test suite | 1,900+ tests (961 Go + 942 Rust) |
| External dependencies | Zero (server), Zero (agent) |
| Check-in DB queries | Zero (FleetIndex) |
| Fleet memory per node | ~388 bytes |

## How It Compares

The following estimates are based on vendor-documented requirements where available and internal benchmarks for Hobnob. All figures assume the same reference hardware: **4 vCPU, 8 GB RAM, SSD, 5-minute check-in interval.** Hobnob has no production deployments — its figures are theoretical projections from measured benchmarks. See the [Technical Comparison](technical-comparison.md) for detailed methodology and vendor source links.

| | Hobnob | Puppet PE | Chef Infra | Ansible AAP | Salt | CFEngine Enterprise |
|---|---|---|---|---|---|---|
| **Estimated nodes on 4v/8G** | 50K-200K* | 100-500 | 2K-4K | 200-400 | 1.2K-5K | ~1,000 |
| **Server language** | Go | Ruby/JRuby | Ruby/Erlang | Python | Python | C + PostgreSQL |
| **Agent language** | Rust | Ruby | Ruby | None (SSH) | Python | C |
| **Agent binary size** | ~5 MB | ~200 MB | ~100 MB | 0 (agentless) | ~50 MB | ~3 MB |
| **Fleet memory/node** | 388 bytes | ~80 KB | ~30 KB | ~100 MB/fork | varies | ~8 MB |
| **Check-in model** | Agent pull (gRPC) | Agent pull (HTTPS) | Agent pull (HTTPS) | Controller push (SSH) | Agent pull+push (ZMQ) | Agent pull (custom) |
| **Config language** | YAML | Puppet DSL | Ruby DSL | YAML + Jinja2 | YAML + Jinja2 | Promise DSL |
| **Transport security** | mTLS + ED25519 | HTTPS + client certs | HTTPS + API tokens | SSH | ZeroMQ + AES | Custom TLS |

*Hobnob's capacity estimate is theoretical — derived from internal FleetIndex benchmarks (~300µs per check-in, ~388 bytes per node) with conservative adjustments for real-world overhead. SQLite write batching is the first bottleneck (~50K); PostgreSQL extends capacity to ~200K. See [Scalability Analysis](../../devdocs/scalability.md) for benchmark data.

**Why the range varies:** Each tool's architecture determines its bottleneck on this hardware. Puppet's catalog compilation requires more CPU and RAM than 4v/8G provides (PE docs specify 10 GB minimum for 100 nodes). CFEngine Enterprise's PostgreSQL reporting hub requires ~8 MB per node (CFEngine docs). Ansible's push model is limited by SSH fork parallelism. Hobnob's in-memory FleetIndex and zero-DB check-in path minimize both CPU and RAM per node.
