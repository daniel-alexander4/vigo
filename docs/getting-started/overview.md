
# Vigo — Technical Overview

Alexander4, LLC | vigo.licensing@alexander4.io

---

## What It Is

Vigo is a pull-based distributed state enforcement engine. Agents on managed nodes pull desired state from a central server, apply changes idempotently, and report results back. Configuration is declared in YAML files. The system continuously converges machines toward the declared state.

## Components

| Binary | Language | Role |
|--------|----------|------|
| `vigosrv` | Go | Server — gRPC (port 1530) + REST API/Web UI (port 8443) |
| `vigo` | Rust | Agent — runs on each managed node |
| `vigocli` | Go | Admin CLI — enrollment, state management, orchestration |

The server uses SQLite (embedded, WAL mode). No external database, message broker, or application runtime is required.

## Supported Platforms

| Platform | Agent | Init System | Package Manager |
|----------|:-----:|-------------|-----------------|
| Linux | Yes | systemd | apt, dnf, yum, zypper, pacman, apk |
| macOS | Yes | launchd | Homebrew |
| FreeBSD | Yes | rc.d | pkg |
| OpenBSD | Yes | rcctl | pkg_add |
| NetBSD | Yes | rc.d | pkgin |
| illumos | Yes | SMF | IPS |
| Windows | Yes | Windows Service | Chocolatey, winget, Scoop |

Network devices (Cisco IOS/NX-OS, Juniper Junos) are managed over SSH via a gateway proxy pattern.

## Key Technical Properties

- One server process handles the entire fleet. No compile masters, worker pools, or database clusters at any scale.
- Agent binary is ~7 MB on x86_64 (~6 MB on arm64), statically compiled, with zero runtime dependencies.
- Check-in operations execute entirely in memory (FleetIndex). Zero database queries on the hot path.
- Every resource executor is enforced-idempotent: check current state, act only on drift, verify after. The agent runs a full convergence pass every check-in — not just once at deploy time. Drift from manual changes, package upgrades, or external automation is corrected within one interval.
- Check-in intervals scale down to **15 seconds** on modest hardware (4 vCPU / 8 GB), enabling continuous enforcement rather than periodic auditing. At 15-second convergence, drift correction is near-instantaneous, stale detection fires within 45 seconds, and config publishes propagate fleet-wide in under 15 seconds. See [Performance Analysis (15s)](performance-15s.md) for capacity projections and use cases this enables — from real-time compliance enforcement to self-healing infrastructure to sub-minute incident response.
- Agents fall back to offline convergence using signed, cached policy bundles when the server is unreachable. Pending results queue locally and drain on reconnect.
- Agents default to stateless polling. Persistent streams are promoted only when the server has work to dispatch, then released when idle.
- Every agent request is ED25519-signed and verified against stored public keys. All traffic is mTLS. There is no plaintext mode.
- Secrets use a `secret:` prefix in YAML, resolved at config load time through a pluggable provider. Secrets never appear in config files, environment variables, logs, database rows, gRPC payloads, or run results.
- Enrollment uses one-time bcrypt-hashed tokens bound to hostname glob patterns.
- The publish pipeline validates configs before they go live: YAML syntax, cross-reference integrity, circular dependency detection, idempotency enforcement, self-protection guardrails, and URL aliveness checks. When validation fails, the built-in AI assistant streams fix suggestions to the terminal.
- Self-protection guardrails block any resource that targets the agent binary, server binary, config directory, or service unit. These checks cannot be bypassed.
- Tamper-evident audit trail: every auditable event is recorded in a SHA-256 hash chain. Any modification or deletion is cryptographically detectable.
- Blast radius protection: auto-rollback if fleet compliance drops below threshold after config publish.
- Peer replication: primary/peer file sync for server redundancy with automatic agent failover and CLI-driven promotion.
- `vigo status` shows the local agent's enforced policy, collected traits, and pending results — fully transparent to the machine's admin.
- Configuration is YAML only. One format, one directory, one publish command. No API writes, no database config, no UI mutations.

## Executors (73 built-in)

### Cross-Platform (25)


file, directory, symlink, exec, process, file_line, blockinfile, replace, field_edit, ini, json_file, host, sshkey, source_package, nonrepo_package, repository, git, pip, gem, npm, docker_compose, docker_container, docker_image, reboot, custom

### Unix (12)

service, package, user, group, cron, hostname, timezone, firewall, sysctl, mount, swap, kernel_module

Each dispatches to the platform-native backend automatically. Write `type: service` and the agent uses systemd on Linux, launchctl on macOS, rcctl on OpenBSD, SMF on illumos, or sc.exe on Windows.

### Linux-Only (5)

tmpfile, logrotate, alternatives, selinux, systemd_dropin

### Windows-Only (22)

**System:** service, package, user, firewall, cron, hostname, timezone, registry, windows_env, reboot_windows, powershell, local_security_policy, domain_membership, windows_update, certificate_windows, windows_defender, service_recovery_windows

### Network Device (16)

ssh_exec, network_config, cisco_vlan, cisco_interface, cisco_acl, junos_interface, network_ntp, network_syslog, network_snmp, network_user, network_banner, network_interface, network_vlan, network_acl, network_static_route, network_dns

## Trait Collectors (40)

os, hardware, network, packages, time, identity, virtual, uptime, loadavg, mountpoints, processors, dmi, cloud, filesystem, kernel, users, memory, blockdevices, docker, systemd, selinux, timezone, locale, swap, gpu, cert_lifecycle, ports, security_scan, tpm, secureboot, display, package_updates, swarm, swarm_seeds, swarmgit, connwaer, package_source_audit, kernel_module_attestation, listeners, cve_running_correlation

Each collector has platform-specific implementations. Traits are available in `when:` conditional expressions and Go templates. The four newest Linux-only collectors (`package_source_audit`, `kernel_module_attestation`, `listeners`, `cve_running_correlation`) cover repo allowlist drift, unsigned kernel modules, port → process attribution, and CVE × running-binary correlation.

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
    target_path: /etc/nginx/nginx.conf
    owner: root
    group: root
    mode: "0644"
    source: templates/nginx.conf.tmpl
    notify: [Restart nginx]

  - name: Restart nginx
    type: service
    service: nginx
    state: restarted
    enabled: "true"
    when: "changed"
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
    target_path: /opt/myapp/docker-compose.yml
    owner: root
    group: root
    mode: "0644"
    content: |
      services:
        web:
          image: nginx:latest
          ports: ["80:80"]
    notify: [App stack]

  - name: App stack
    type: docker_compose
    compose_file: /opt/myapp/docker-compose.yml
    pull: always
    when: "changed"
```

## Orchestration

- **Tasks** — push ad-hoc commands to targeted agents with real-time result streaming
- **Workflows** — chain tasks into multi-step sequences with conditional branching
- **Rolling execution** — split targets into batches with health checks between rounds
- **Live queries** — run read-only commands across the fleet and aggregate results

## Observability

- Web UI (HTMX) — fleet dashboard, security posture, compliance gauges, run history, task/workflow execution, spanner status
- Prometheus metrics — 50+ gauges, counters, histograms at `GET /metrics`
- Security scanning — CVE detection (Trivy, debsecan, Windows Update), hardening audits (Lynis), rootkit detection (rkhunter, chkrootkit), file integrity (AIDE)
- Convergence monitoring — converged / relapsed / diverged / degraded / failed / offline / no data classification per node
- Compliance export — SIEM, CMDB, OSCAL endpoints (independently toggleable)
- Risk scoring — per-envoy risk quantification based on CVEs, hardening, patch age, and exposure
- Webhooks — HMAC-SHA256 signed, retry with backoff
- Email notifications — stale nodes, compliance thresholds, secret rotation, config reload failures
- Audit log — config changes, enrollments, revocations, task dispatches

## Spanner

Hub-spoke topology for multi-site deployments. The hub routes enrollment to the nearest spoke by hostname pattern. Admin queries fan out from hub to all spokes. Per-spoke compliance breakdown and latency metrics. Envoy reassignment between spokes with zero re-enrollment. Spoke drain for maintenance.

## Requirements

- **Server**: Linux host with Docker, or Go 1.25+ for source builds. Ports 1530 (gRPC) and 8443 (REST/UI).
- **Agent**: no runtime dependencies. ~7 MB static binary on x86_64 (~6 MB on arm64). Bootstrap: `curl -sSfk https://server:8443/bootstrap | sudo sh`
- **Database**: SQLite (embedded, zero admin). Optional PostgreSQL support.

## Contact

Alexander4, LLC
vigo.licensing@alexander4.io
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
