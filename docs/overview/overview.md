# Hobnob — Technical Overview

Alexander4, LLC | hobnob.licensing@alexander4.org

---

## What It Is

Hobnob is a pull-based configuration management system. Agents on managed nodes pull desired state from a central server, apply changes idempotently, and report results back. Configuration is declared in YAML files. The system continuously converges machines toward the declared state.

## Components

| Binary | Language | Role |
|--------|----------|------|
| `hobd` | Go | Server — gRPC (port 1530) + REST API/Web UI (port 8443) |
| `hobnob` | Rust | Agent — runs on each managed node |
| `hobnobcli` | Go | Admin CLI — enrollment, config management, orchestration |

The server uses SQLite (embedded, WAL mode). No external database, message broker, or application runtime is required.

## Supported Platforms

| Platform | Agent | Init System | Package Manager |
|----------|:-----:|-------------|-----------------|
| Linux | Yes | systemd | apt, dnf, yum |
| macOS | Yes | launchd | Homebrew |
| FreeBSD | Yes | rc.d | pkg |
| OpenBSD | Yes | rcctl | pkg_add |
| NetBSD | Yes | rc.d | pkgin |
| illumos | Yes | SMF | IPS |
| Windows | Yes | Windows Service | Chocolatey, winget, Scoop |

Network devices (Cisco IOS/NX-OS, Juniper Junos) are managed over SSH via a gateway proxy pattern.

## Key Technical Properties

- One server process handles the entire fleet. No compile masters, worker pools, or database clusters at any scale.
- Agent binary is ~5 MB, statically compiled, with zero runtime dependencies.
- Check-in operations execute entirely in memory (FleetIndex). Zero database queries on the hot path.
- Agents fall back to offline convergence using signed, cached policy bundles when the server is unreachable. Pending results queue locally and drain on reconnect.
- Agents default to stateless polling. Persistent streams are promoted only when the server has work to dispatch, then released when idle.
- Every agent request is ED25519-signed and verified against stored public keys. All traffic is mTLS. There is no plaintext mode.
- Secrets use a `secret:` prefix in YAML, resolved at config load time through a pluggable provider. Secrets never appear in config files, environment variables, logs, database rows, gRPC payloads, or run results.
- Enrollment uses one-time bcrypt-hashed tokens bound to hostname glob patterns.
- The publish pipeline validates configs before they go live: YAML syntax, cross-reference integrity, circular dependency detection, idempotency enforcement, self-protection guardrails, and URL aliveness checks. When validation fails, the built-in AI assistant streams fix suggestions to the terminal.
- Self-protection guardrails block any resource that targets the agent binary, server binary, config directory, or service unit. These checks cannot be bypassed.
- Configuration is YAML only. One format, one directory, one publish command. No API writes, no database config, no UI mutations.

## Executors (74 built-in)

### Cross-Platform (24)

file, directory, symlink, exec, process, file_line, blockinfile, replace, field_edit, ini, json_file, host, sshkey, archive, repository, git, pip, gem, npm, docker_compose, docker_container, docker_image, reboot, custom

### Unix (12)

service, package, user, group, cron, hostname, timezone, firewall, sysctl, mount, swap, kernel_module

Each dispatches to the platform-native backend automatically. Write `type: service` and the agent uses systemd on Linux, launchctl on macOS, rcctl on OpenBSD, SMF on illumos, or sc.exe on Windows.

### Linux-Only (5)

tmpfile, logrotate, alternatives, selinux, systemd_dropin

### Windows-Only (22)

**System:** service, package, user, firewall, cron, hostname, timezone, registry, acl, windows_feature, windows_env, reboot_windows, powershell, local_security_policy, domain_membership, windows_update, certificate_windows, windows_defender, service_recovery_windows

**IIS:** iis_site, iis_app_pool

**Network:** smb_share

### Network Device (11)

ssh_exec, network_config, cisco_vlan, cisco_interface, cisco_acl, junos_interface, network_ntp, network_syslog, network_snmp, network_user, network_banner

## Trait Collectors (27)

os, hardware, network, packages, time, identity, virtual, uptime, loadavg, mountpoints, processors, dmi, cloud, filesystem, kernel, users, memory, blockdevices, docker, systemd, selinux, timezone, locale, swap, gpu, cert_expiry, ports

Each collector has platform-specific implementations. Traits are available in `when:` conditional expressions and Go templates.

## Sample Resource Definitions

### Package + service + config file

```yaml
name: webserver
resources:
  - name: Install nginx
    type: package
    package: nginx

  - name: Nginx config
    type: file
    targetpath: /etc/nginx/nginx.conf
    owner: root
    group: root
    mode: "0644"
    source: templates/nginx.conf.tmpl
    notify: Restart nginx

  - name: Restart nginx
    type: service
    service: nginx
    state: restarted
    enabled: "true"
    subscribes: Nginx config
```

### Conditional resources

```yaml
resources:
  - name: Install apt-transport-https
    type: package
    package: apt-transport-https
    when: "os_family('debian')"

  - name: Install epel-release
    type: package
    package: epel-release
    when: "os_family('redhat')"
```

### Docker Compose stack

```yaml
resources:
  - name: App compose file
    type: file
    targetpath: /opt/myapp/docker-compose.yml
    owner: root
    group: root
    mode: "0644"
    content: |
      services:
        web:
          image: nginx:latest
          ports: ["80:80"]
    notify: App stack

  - name: App stack
    type: docker_compose
    compose_file: /opt/myapp/docker-compose.yml
    pull: always
    subscribes: App compose file
```

## Orchestration

- **Tasks** — push ad-hoc commands to targeted agents with real-time result streaming
- **Workflows** — chain tasks into multi-step sequences with conditional branching
- **Rolling execution** — split targets into batches with health checks between rounds
- **Live queries** — run read-only commands across the fleet and aggregate results

## Observability

- Web UI (HTMX) — fleet dashboard, compliance gauges, run history, task/workflow execution, spanner status
- Prometheus metrics — 20+ gauges, counters, histograms at `GET /metrics`
- Compliance monitoring — compliant / drifted / failing / stale / unknown classification per node
- Compliance export — SIEM, CMDB, OSCAL endpoints (independently toggleable)
- Webhooks — HMAC-SHA256 signed, retry with backoff
- Email notifications — stale nodes, compliance thresholds, secret rotation, config reload failures
- Audit log — config changes, enrollments, revocations, task executions

## Spanner

Hub-spoke topology for multi-site deployments. The hub routes enrollment to the nearest spoke by hostname pattern. Admin queries fan out from hub to all spokes. Per-spoke compliance breakdown and latency metrics. Thrall reassignment between spokes with zero re-enrollment. Spoke drain for maintenance.

## Licensing

Split-license model:
- **Agent**: source-available under the Elastic License 2.0. Readable and auditable.
- **Server and CLI**: proprietary. Distributed as compiled binaries and container images.

All features are included at every tier. No enterprise-only features, no gated add-ons.

### Pricing (per node, per year, cumulative tiers)

| Tier | Nodes | Per Node / Year |
|------|-------|-----------------|
| Community | 1 – 25 | Free |
| Starter | 26 – 250 | $84 |
| Growth | 251 – 1,000 | $74 |
| Business | 1,001 – 5,000 | $64 |
| Enterprise | 5,001 – 25,000 | $54 |
| Scale | 25,001 – 100,000 | $41 |
| Unlimited | 100,001+ | $31 |

Billed annually. Cumulative tiers — each band prices only the nodes in that band.

### Loyalty Discount

After year 5 of continuous renewal, per-node rates decrease automatically: 10% at year 6, increasing annually to a floor of 50% at year 11+. The discount applies across all tiers.

### License Enforcement

If node count exceeds the license limit: 15-day grace period (warnings only) followed by 15-day enrollment block (existing nodes unaffected, new enrollments rejected), then server shutdown. Existing nodes are never locked out. Returning to compliance resets the grace clock immediately.

## Support

| Level | Included With | Response Time | Channel |
|-------|---------------|---------------|---------|
| Community | Community tier | Best effort | GitHub Issues |
| Standard | Starter, Growth | 2 business days | Email |
| Priority | Business+ | 1 business day | Email + direct |
| Dedicated | Enterprise+ | 4 business hours | Email + direct + scheduled calls |

Support covers configuration guidance, bug triage, and upgrade assistance. Custom development is available as a separate engagement.

## Requirements

- **Server**: Linux host with Docker, or Go 1.25+ for source builds. Ports 1530 (gRPC) and 8181 (REST/UI).
- **Agent**: no runtime dependencies. ~5 MB static binary. Bootstrap: `curl -sSfk https://server:8443/bootstrap | sudo sh`
- **Database**: SQLite (embedded, zero admin). Optional PostgreSQL support.

## Contact

Alexander4, LLC
hobnob.licensing@alexander4.org
