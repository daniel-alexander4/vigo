# Use Cases

Hobnob is a general-purpose distributed state enforcement engine. Its core pattern — central authority defines desired state, distributed agents enforce it idempotently, report back with collected traits, all over authenticated channels with offline resilience — applies across many domains.

## Configuration Management

The foundational use case. Define desired state for packages, files, services, users, firewall rules, and repositories. Agents check before acting, apply only what's drifted, and report results. Notify/subscribes triggers handle cascading changes (e.g., config file change restarts the service).

## Security & Compliance Enforcement

- **CIS/STIG hardening** — Desired state isn't just "install nginx" but "ensure `/etc/ssh/sshd_config` has `PermitRootLogin no`." The agent checks before acting and reports drift. A compliance module library turns Hobnob into a continuous audit engine that fixes what it finds.
- **Zero-trust endpoint posture** — Every 15-30 seconds, the server knows every machine's patch level, open ports, running processes, and firewall rules. Tie trait data to network access decisions. Stale machines get quarantined.
- **Supply chain integrity** — Agents verify binary checksums, package signatures, and running process hashes against a golden manifest. Drift means someone changed something they shouldn't have.

## Edge & IoT Fleet Management

- **Retail / POS terminals** — Thousands of kiosks, ATMs, or point-of-sale systems across locations. Push config, enforce PCI compliance, collect hardware health traits, dispatch firmware updates. Offline convergence means a store with flaky internet still enforces policy from cache.
- **Digital signage** — Desired state includes content playlists, display schedules, and network config. Traits report screen resolution, uptime, and temperature. Task dispatch: "take a screenshot and send it back."
- **Industrial sensors / PLCs** — The agent is 5 MB and runs on ARM. Configuration of thresholds, collection intervals, and alert rules. Traits report sensor readings. Hub-spoke sharding maps naturally to plant, floor, and line hierarchies.

## Network Device Management

With 16 built-in network device executors, Hobnob is a full network automation platform. Switch configs, firewall rules, VLAN assignments, and ACLs defined as desired state. At 15-second check-in intervals, a switch going dark is detected within 45 seconds.

## Disaster Recovery & Incident Response

- **Pre-staged recovery playbooks** — Define "disaster recovery mode" as a set of modules. Flip a config flag, publish, and within 15 seconds every machine in the fleet is executing the DR playbook. Offline convergence means machines in a degraded datacenter still act on the last-known plan.
- **Incident containment** — Task dispatch: "kill process X, block IP Y, rotate credential Z" across 10,000 machines simultaneously. Results stream back in real-time via the orchestration layer.

## Software Distribution & Update Management

- **Controlled rollouts** — Rolling execution with batch planning and health checks between rounds. Combined with `when:` conditionals, you can do canary deploys: "update the package on 5% of nodes where `environment == staging`", verify via traits, then widen.
- **Embedded device OTA** — The agent checks in, gets told there's a new firmware version, downloads it, applies it, reboots, checks in again. The server tracks which devices are on which version via traits.

## Observability & Inventory

- **Real-time fleet inventory** — Trait collectors discover OS, hardware, packages, users, network interfaces, mounts, and services. At 15-second intervals, this is a live CMDB that's always accurate. No scanning, no staleness, no drift between inventory and reality.
- **Lightweight monitoring** — Traits can include disk usage, memory pressure, process counts, and certificate expiry dates. The server has compliance thresholds, SMTP alerting, and webhook notifications. Not a replacement for Prometheus, but for environments where "is the disk full and is the cert expiring" is 90% of the monitoring need, it covers it.

## Education & Lab Management

Push a lab configuration (users, packages, firewall rules, shared mounts) to 50 workstations. Reset them between classes. Traits report who's logged in and what's running. At 15-second intervals, a TA can watch the dashboard and see machines come up in real time.

## Why These Work

The primitives that make all of this possible:

| Primitive | What it enables |
|-----------|----------------|
| Idempotent resources | Safe to re-apply indefinitely. Check before act. No side effects on no-op. |
| Trait collection | Real-time fleet-wide situational awareness. Decisions based on actual state, not assumed state. |
| Desired-state convergence | Define intent, not procedure. The system figures out how to get there. |
| Offline resilience | Agents converge locally from cached policy. Works through network partitions. |
| Authenticated channels | mTLS + ED25519 signatures on every request. Non-negotiable for security-sensitive use cases. |
| Task dispatch | Ad-hoc imperative commands when desired-state isn't the right model. |
| Rolling execution | Safe fleet-wide operations with health checks and abort conditions. |
| Hub-spoke sharding | Scales horizontally. Maps to organizational or geographic boundaries. |
| `when:` conditionals | Same policy, different behavior per OS, environment, role, or any trait. |

Hobnob is not a configuration management tool that happens to do other things. It is a distributed state enforcement engine whose first application is configuration management.
