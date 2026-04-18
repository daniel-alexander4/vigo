
# Concepts

Core concepts that explain how Vigo works.

## Architecture & Lifecycle

- [Architecture](architecture.md) — Three binaries, ports, data flows
- [Check-in Lifecycle](check-in-lifecycle.md) — Pull loop, fingerprint, delta transfer
- [Convergence Walkthrough](convergence-walkthrough.md) — End-to-end example with timing and traffic flow
- [Compiled Promises](compiled-promises.md) — Bundle signing, TTL, offline convergence

## Configuration

- [Config Format](config-format.md) — Modules, roles, envoys.vgo, vars, environment_overrides
- [Resource DAG](resource-dag.md) — depends_on, notify, subscribes
- [Resource Language](resource-language.md) — defaults, foreach, case/match, conditional_block
- [When Expressions](when-expressions.md) — Boolean logic and 15 builtin functions
- [Templates](templates.md) — Go templates in content: attributes
- [Secrets](secrets.md) — Two backends and the secret: prefix
- [Composition Patterns](composition-patterns.md) — Reuse patterns: roles, includes, inheritance, vars_from
- [Multi-Axis Configuration](multi-axis-config.md) — Per-environment overrides for multi-site fleets
- [Lookup Tables](lookups.md) — Per-envoy resource field variation via tag/host/trait keys
- [User Management](user-management.md) — Why user accounts need per-user modules, directory-level compliance, and two-phase retirement

## Scaling & Distribution

- [Spanner](spanner.md) — Hub-spoke scaling
- [P2P Spanner](p2p-spanner.md) — Peer-to-peer spanner architecture
- [Swarm](swarm.md) — Peer-to-peer content distribution
- [Vigosync](vigosync.md) — Per-user file sync across machines
- [Vigorepo](vigorepo.md) — Personal-DR git mirroring with bundle-based sync and server-coordinated mirror selection
- [Environment Isolation](environment-isolation.md) — Separating dev/staging/prod and intra-environment tiers

## Compliance & Safety

- [Compliance](compliance.md) — Per-resource to fleet-wide compliance
- [Risk Scoring](risk-scoring.md) — Per-envoy and fleet-wide risk scores from security scans, convergence, and connectivity
- [Observe Mode](observe-mode.md) — Global and per-entry drift-only mode
- [Guardrails](guardrails.md) — Self-protection for agent and server paths
- [Stream-Edit](stream-edit.md) — Post-process file resource content through agent-local scripts (with safety model)
- [Module Retraction](module-retraction.md) — Detecting removed modules and generating idempotent retract files

## Fleet Patterns

- [Multi-Client Environments](multi-client-environments.md) — MSP pattern: clients x environments with shared roles
- [Network Devices](network-devices.md) — Managing switches and routers via SSH gateway proxy
- [Scrier](scrier.md) — Admin remote access (SSH, RDP, VNC) over the envoy's outbound tunnel — no inbound ports, no bastion

## Extensibility

- [Custom Executors](custom-executors.md) — JSON stdin/stdout protocol
- [Custom Traits](custom-traits.md) — Custom trait collector scripts
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
