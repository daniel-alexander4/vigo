
# Use Cases

Vigo is a general-purpose distributed state enforcement engine. Its core pattern — central authority defines desired state, distributed agents enforce it idempotently, report back with collected traits, all over authenticated channels with offline resilience — applies across many domains.

Every use case below works at any check-in interval. The difference is operator experience: how quickly drift is corrected, how fast stale machines are flagged, and whether the dashboard feels like a live terminal or a batch report. The tiers below reflect where each use case starts to feel *right*.

## Works Well at Any Interval (1-5 minutes)

These use cases are inherently asynchronous. The work happens in the background, and whether it completes in 30 seconds or 5 minutes doesn't change the outcome.

### Configuration Management

The foundational use case. Define desired state for packages, files, services, users, firewall rules, and repositories. Agents check before acting, apply only what's drifted, and report results. Notify/subscribes triggers handle cascading changes (e.g., config file change restarts the service).

Drift correction at 5-minute intervals is fine — the machine was misconfigured for hours or days before Vigo was deployed. Another 5 minutes is irrelevant.

### Software Distribution & Update Management

- **Controlled rollouts** — Rolling execution with batch planning and health checks between rounds. Combined with `when:` conditionals, you can do canary deploys: "update the package on 5% of nodes where `environment == staging`", verify via traits, then widen.
- **Embedded device OTA** — The agent checks in, gets told there's a new firmware version, downloads it, applies it, reboots, checks in again. The server tracks which devices are on which version via traits.

Rollouts are inherently paced — health checks between batches take longer than the check-in interval. A 5-minute interval adds at most 5 minutes to a process that already takes 30-60 minutes for a fleet-wide rollout.

### Edge & IoT Fleet Management

- **Retail / POS terminals** — Thousands of kiosks, ATMs, or point-of-sale systems across locations. Push config, enforce PCI compliance, collect hardware health traits, dispatch firmware updates. Offline convergence means a store with flaky internet still enforces policy from cache.
- **Digital signage** — Desired state includes content playlists, display schedules, and network config. Traits report screen resolution, uptime, and temperature.
- **Industrial sensors / PLCs** — The agent is ~6 MB on arm64 (~7 MB on x86_64) and runs on ARM. Configuration of thresholds, collection intervals, and alert rules. Traits report sensor readings. Hub-spoke sharding maps naturally to plant, floor, and line hierarchies.

These devices are deployed and mostly left alone. Configuration changes are infrequent. The value is in enforcement and trait collection, not speed.

## Better at 30 Seconds

These use cases work at longer intervals but start to feel sluggish. At 30-second check-ins, drift correction happens within 30 seconds, stale detection within 90 seconds, and the dashboard feels alive.

### Security & Compliance Enforcement

- **CIS/STIG hardening** — Desired state isn't just "install nginx" but "ensure `/etc/ssh/sshd_config` has `PermitRootLogin no`." The agent checks before acting and reports drift. A compliance module library turns Vigo into a continuous audit engine that fixes what it finds.
- **Zero-trust endpoint posture** — Every 30 seconds, the server knows every machine's patch level, open ports, running processes, and firewall rules. Tie trait data to network access decisions. Stale machines get quarantined.
- **Supply chain integrity** — Agents verify binary checksums, package signatures, and running process hashes against a golden manifest. Drift means someone changed something they shouldn't have.

At 5-minute intervals, a compromised machine could run unauthorized binaries for 5 minutes before the next check-in detects the change. At 30 seconds, the window shrinks to 30 seconds. For compliance frameworks that require "continuous monitoring," 30-second intervals demonstrate continuous enforcement rather than periodic scanning.

### Network Device Management

With 16 built-in network device executors, Vigo is a full network automation platform. Switch configs, firewall rules, VLAN assignments, and ACLs defined as desired state.

At 30-second intervals, a switch going dark is detected within 90 seconds (3x interval stale threshold). At 5 minutes, a dead switch goes unnoticed for 15 minutes. For network operations teams watching a dashboard, 90-second stale detection is the difference between "we caught it" and "a user called to tell us."

### Observability & Inventory

- **Real-time fleet inventory** — Trait collectors discover OS, hardware, packages, users, network interfaces, mounts, and services. At 30-second intervals, this is a live CMDB that's always accurate. No scanning, no staleness, no drift between inventory and reality.
- **Lightweight monitoring** — Traits can include disk usage, memory pressure, process counts, and certificate expiry dates. The server has compliance thresholds, SMTP alerting, and webhook notifications. Not a replacement for Prometheus, but for environments where "is the disk full and is the cert expiring" is 90% of the monitoring need, it covers it.

At 5-minute intervals, the inventory is a snapshot that's always 0-5 minutes stale. At 30 seconds, it's close enough to real-time that operators trust it for live decisions — "which machines have this package installed right now?"

## Best at 15 Seconds

These use cases involve humans watching dashboards and waiting for results. The difference between 15-second and 30-second intervals is the difference between "that was fast" and "I can see it working." Below 15 seconds, streaming is a better model.


### Disaster Recovery & Incident Response

- **Pre-staged recovery playbooks** — Define "disaster recovery mode" as a set of modules. Flip a config flag, publish, and within 15 seconds every machine in the fleet is executing the DR playbook. Offline convergence means machines in a degraded datacenter still act on the last-known plan.
- **Incident containment** — Task dispatch: "kill process X, block IP Y, rotate credential Z" across 10,000 machines simultaneously. Results stream back in real-time via the orchestration layer.

During an active incident, every second matters. At 5-minute intervals, you publish containment policy and then stare at the dashboard for up to 5 minutes wondering if it worked. At 15 seconds, you see machines converge before your adrenaline subsides. At 30 seconds, it's acceptable but feels slow when you're under pressure.

Note: task dispatch via streaming is instant regardless of check-in interval. The 15-second interval matters for the *promotion latency* — how long until a polling agent opens a stream after the server requests it.

### Education & Lab Management

Push a lab configuration (users, packages, firewall rules, shared mounts) to 50 workstations. Reset them between classes. Traits report who's logged in and what's running.

A TA standing in front of a class needs to see machines come up in real time. At 15-second intervals, the dashboard updates feel live. At 1 minute, there's an awkward pause. At 5 minutes, the TA has lost the room. The fleet is small (50-200 machines), so the server load of 15-second intervals is trivial.

## Interval Selection Guide

| Use Case | Minimum Practical | Recommended | Why |
|----------|-------------------|-------------|-----|
| Configuration management | 5 min | 1-5 min | Drift was there for days; 5 more minutes doesn't matter |
| Software rollouts | 5 min | 1-5 min | Rollout pace is health-check-limited, not interval-limited |
| Edge / IoT | 1 min | 1-5 min | Devices are remote; config changes are rare |
| Security / compliance | 1 min | 30 sec | Shrinks the unauthorized-state window |
| Network device management | 1 min | 30 sec | Stale detection speed matters for NOC dashboards |
| Fleet inventory / monitoring | 1 min | 30 sec | Operators need to trust the data is current |
| Incident response | 30 sec | 15 sec | Every second counts during containment |
| Lab management | 30 sec | 15 sec | Humans are watching and waiting |

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

Vigo is not a configuration management tool that happens to do other things. It is a distributed state enforcement engine whose first application is configuration management.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
