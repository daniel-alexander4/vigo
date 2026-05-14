
# Overview

Vigo keeps managed nodes in the state you've declared. Agents on each node pull desired state from a central server, apply changes idempotently, and report results back. You write the state in YAML; the system converges machines toward it on a fixed interval — and stays there.

## Three binaries

| Binary | Where it runs | What it does |
|---|---|---|
| `vigosrv` | One central server | Holds the desired-state config, signs and serves it, accepts results. gRPC on port 1530, REST + Web UI on port 8443. SQLite-backed; no separate database to operate. |
| `vigo` | Every managed node ("envoy") | Pulls policy, applies it, reports back. ~7 MB static Rust binary, no runtime deps. |
| `vigocli` | Wherever you administer from | Talks to the server's REST API: enroll, query, publish config, dispatch tasks, open Scrier sessions, run reports. |

## Supported platforms

Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos, Windows. Per-platform init systems (systemd / launchd / rc.d / rcctl / SMF / Windows Service) and package managers (apt, dnf, yum, zypper, pacman, apk, Homebrew, pkg, pkgin, IPS, Chocolatey, winget, Scoop) are first-class. Network devices (Cisco IOS/NX-OS, Juniper Junos) work over SSH via a gateway envoy.

## Mental model

1. **Declare state in YAML files** under `/srv/vigo/stacks/stockpile/`. Hostname patterns match envoys to roles, roles aggregate modules, modules describe resources (files, packages, services, users, repositories, exec actions). Per-host overrides, environment splits, and conditional logic compose on top.
2. **Publish.** `vigocli config publish` validates the tree, syncs it to `/srv/vigo/stacks/.live/`, and triggers a server reload.
3. **Agents converge.** Every check-in interval — as short as 15 seconds on modest hardware — each envoy pulls the policy matching its hostname, applies any drift idempotently, and reports per-resource outcomes. Drift from manual changes, package upgrades, or external automation is corrected within one interval.
4. **Observe.** The Web UI shows fleet convergence, drift, compliance posture, risk scoring, audit history, and a per-envoy detail view. The CLI gives you the same data plus orchestration verbs (task dispatch, workflow execution, ad-hoc query, Scrier shells).

## What sets Vigo apart from other config-management

- **One server, whole fleet.** No compile masters, worker pools, or database clusters at any scale. SQLite + WAL + optional Litestream WAL replication.
- **Continuous, not periodic.** The agent runs a full convergence pass every check-in. Down to 15 seconds on the right hardware. Manual changes get corrected in seconds.
- **Offline-safe.** Agents cache the last signed policy bundle. When the server is unreachable they re-apply that bundle against fresh traits, queue results, and flush on reconnect.
- **Hardened by construction.** mTLS everywhere, every agent request ed25519-signed, secrets resolved through a `secret:` prefix that never lands in config / env / DB / logs / RPC payloads, a tamper-evident SHA-256-hash-chained audit log, and self-protection guardrails that block any resource targeting the agent or server binaries.
- **Built-in remote access.** Scrier gives you browser-based and CLI SSH/RDP/VNC sessions to any enrolled envoy without opening an inbound port; the envoy's outbound check-in connection carries the bytes back.
- **Built-in compliance reporting.** Modules carry `compliance:` tags; the server unions them against the fleet to produce per-framework coverage reports (HIPAA, SOC 2, PCI-DSS, NIST 800-53, ISO 27001, and more).
- **P2P content distribution.** A swarm substrate moves binaries, container images, git repos, and per-user encrypted directories between envoys directly, without bouncing through the server.

## Where to go next

- **First-time setup:** [Quickstart](quickstart.md) walks install → enroll → first module in ~15 minutes.
- **A specific task:** [How-to guides](howto/) covers install, enrollment, writing modules, publishing, Scrier, swarm, auth, monitoring, backup/DR, compliance, troubleshooting, and migrating from Puppet/Chef/Ansible/Salt/CFEngine.
- **Looking something up:** [Reference](reference/) holds `vigocli` command pages, agent resource and trait pages, the REST API, the server.yaml schema, the module-writing guide, and the compliance-framework matrix.
- **What things mean:** [Glossary](glossary.md).
- **Why decisions were made:** [ADRs](adr/) — historical decision archive, not operator docs.

---
*Confidential — Alexander4, LLC. Not for redistribution. See [legal/license.md](legal/license.md).*
