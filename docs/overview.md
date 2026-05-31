
# Overview

Vigo keeps envoys in the state you've declared. Agents on each envoy pull desired state from a central server, apply changes idempotently, and report results back. You write the state in YAML; the system converges envoys toward it on a fixed interval — and stays there.

That convergence engine is the core, but Vigo is a good deal more than configuration management. The same server that enforces state also scores regulatory compliance, runs active verification (Connwaer), brokers browser SSH/RDP/VNC without inbound ports (Scrier), distributes content peer-to-peer, and holds tens of thousands of nodes per host at a one-second check-in cadence. The sections below start with the convergence model; the rest of the platform layers on from there.

## Three binaries

| Binary | Where it runs | What it does |
|---|---|---|
| `vigosrv` | One central server | Holds the desired-state config, signs and serves it, accepts results. gRPC on port 1530, REST + Web UI on port 8443. SQLite-backed; no separate database to operate. |
| `vigo` | Every envoy (the managed-node agent) | Pulls policy, applies it, reports back. ~7 MB static Rust binary, no runtime deps. |
| `vigocli` | Wherever you administer from | Talks to the server's REST API: enroll, query, publish config, dispatch tasks, open Scrier sessions, run reports. |

## Supported platforms

Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos, Windows. Per-platform init systems (systemd / launchd / rc.d / rcctl / SMF / Windows Service) and package managers (apt, dnf, yum, zypper, pacman, apk, Homebrew, pkg, pkgin, IPS, Chocolatey, winget, Scoop) are supported. Network devices (Cisco IOS/NX-OS, Juniper Junos) work over SSH via a gateway envoy.

## Mental model

1. **Declare state in YAML files** under `/srv/vigo/stacks/stockpile/`. Hostname patterns match envoys to roles, roles aggregate modules, modules describe resources (files, packages, services, users, repositories, exec actions). Per-host overrides, environment splits, and conditional logic compose on top.
2. **Publish.** `vigocli config publish` validates the tree, syncs it to `/srv/vigo/stacks/.live/`, and triggers a server reload.
3. **Agents converge.** Every check-in interval — default 5 minutes; configurable shorter on capable hardware — each envoy pulls the modules matching its hostname, applies any drift idempotently, and reports per-resource outcomes. Drift from manual changes, package upgrades, or external automation is corrected within one interval. (Per-fleet capacity at low intervals is workload-dependent; see your own benchmarks rather than relying on a static "X seconds works" claim.)
4. **Observe.** The Web UI shows fleet convergence, drift, compliance posture, risk scoring, audit history, and a per-envoy detail view. The CLI gives you the same data plus orchestration verbs (task dispatch, workflow execution, ad-hoc query, Scrier shells).

## Key properties you'll encounter

- **One server, whole fleet.** No compile masters, worker pools, or database clusters at any scale. SQLite + WAL + optional Litestream WAL replication to S3-compatible storage.
- **Scales to tens of thousands of nodes per server.** One vigosrv holds ~60k concurrent agent streams on a 31 GiB host — memory-bound and linear (~220 KB/node), CPU never the limit, with graceful shedding rather than collapse under overload. Past that, peer-equal `spanner` federation partitions the fleet across hosts (no central compile to bottleneck). See [Sizing](howto/sizing.md).
- **Continuous convergence.** Every check-in runs a full convergence pass, not just diff-apply. Drift correction is interval-bounded, not on-deploy-only.
- **Offline-safe agents.** Each envoy caches the last signed policy bundle. When the server is unreachable, the agent re-applies that bundle against fresh traits, queues results locally, and flushes them on reconnect. Bundles expire when a `bundle_max_age` is set in `server.yaml`.
- **Mandatory mTLS, mandatory signing.** Every gRPC call rides mTLS. Every agent request is ed25519-signed against the stored pubkey for that envoy. No plaintext mode, no signature-skip mode.
- **Secrets stay out of every visible surface.** A `secret:` prefix in YAML is resolved at config load; the value never appears in config files, environment variables, logs, the database, gRPC payloads, or run results. Provider plug-ins resolve the prefix (`local` file-backed by default, `isopass` HTTP-backed in production).
- **Tamper-evident audit log.** Every auditable event lands in a SHA-256-hash-chained log; modifying or deleting a row is cryptographically detectable.
- **Self-protection guardrails.** The agent refuses to touch its own binary, the server's binary, the config directory, or the agent's service unit, regardless of what a module declares. These checks aren't overridable.
- **Scrier remote access without inbound ports.** SSH, RDP, and VNC sessions to any enrolled envoy ride the same outbound gRPC connection the agent uses for check-in. No port-22 exposure, no bastion, no VPN. See [Set up Scrier](howto/set-up-scrier.md).
- **Compliance matrix derived from modules.** Modules carry `compliance:` tags; the server produces per-framework coverage reports (HIPAA, SOC 2, PCI-DSS, NIST 800-53, ISO 27001, more). See [Compliance reporting](howto/compliance-reporting.md).
- **P2P content distribution.** A swarm substrate moves files, container images, git repos, and per-user encrypted directories between envoys directly. See [Set up Swarm](howto/set-up-swarm.md).

## Limits to know up front

- **Pre-1.0.** APIs and YAML schema can change between minor versions; no backward-compatibility shims. Read the changelog before upgrading; pin versions in production until the project hits 1.0.
- **One server process per control plane.** High-availability is via a primary→secondary peer-replication topology in `server/peer/`, plus optional hub + bolts sharding (`server/spanner/`) for very large fleets. No multi-master.
- **Linux + macOS first.** New agent subsystems ship Linux and macOS at launch; Windows / BSD / illumos parity is a follow-up, prioritized against the rest of the backlog (per the Linux-first doctrine).
- **No session recording (yet).** Scrier's `recording_enabled` is a schema placeholder; the relay does not currently write recordings.
- **No SCP/file-transfer over Scrier.** Interactive shell only.
- **Wayland VNC unsupported.** No reliable Wayland-native VNC server exists at time of writing.
- **Sandgorgon (bare-metal lifecycle).** Deferred to post-v1. The code is in-repo but not a v1-supported feature.
- **Compliance claims are scoped to what the agent + Connwaer can prove.** Process controls, physical-security controls, organizational controls, and sub-millisecond enforcement (financial-services kill switches) are explicitly out of scope. See [reference/compliance-matrix.md → "What's deliberately not claimed"](reference/compliance-matrix.md).

## Where to go next

- **First-time setup:** [Quickstart](quickstart.md) walks install → enroll → first module.
- **A specific task:** [How-to guides](howto/) covers install, enrollment, writing modules, publishing, Scrier, swarm, auth, monitoring, backup/DR, compliance, troubleshooting, and migrating from Puppet/Chef/Ansible/Salt/CFEngine.
- **Looking something up:** [Reference](reference/) holds `vigocli` command pages, agent resource and trait pages, the REST API, the server.yaml schema, the module-writing guide, and the compliance-framework matrix.
- **What things mean:** [Glossary](glossary.md).
- **Why decisions were made:** [ADRs](adr/) — historical decision archive, not operator docs.

---
*Confidential — Alexander4, LLC. Not for redistribution. See [legal/license.md](legal/license.md).*
