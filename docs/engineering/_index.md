
# Engineering

The engineer's guide to Vigo: how the system works, how to write modules, and how to use the optional subsystems. For day-one onboarding, see [Getting Started](../getting-started/_index.md). For CLI and resource-type lookups, see [Reference](../reference/_index.md). For operational runbooks, see [Operations](../operations/_index.md).

## Architecture

How Vigo's internals work. Start here when onboarding into a running fleet or debugging behavior.

- [Overview](architecture/overview.md) — Three binaries, ports, data flows
- [Check-in Lifecycle](architecture/check-in-lifecycle.md) — Pull loop, fingerprint, delta transfer
- [Convergence Walkthrough](architecture/convergence-walkthrough.md) — End-to-end with timing and traffic
- [Resource DAG](architecture/resource-dag.md) — depends_on, notify, subscribes
- [Offline Bundles](architecture/offline-bundles.md) — Signed policy bundles for offline convergence

## Writing Modules

Reference for authoring `.vgo` configs.

- [Resource Types](writing-modules/resource-types.md) — foreach, case/match, conditional_block, defaults
- [When Expressions](writing-modules/when-expressions.md) — Boolean logic and builtin functions
- [Lookups](writing-modules/lookups.md) — Per-envoy resource field variation via tag/host/trait keys
- [Templates](writing-modules/templates.md) — Go templates in `content:` attributes
- [Multi-Axis Config](writing-modules/multi-axis-config.md) — Per-environment overrides for multi-site fleets
- [Composition Patterns](writing-modules/composition-patterns.md) — Roles, includes, directory inheritance
- [Retraction](writing-modules/retraction.md) — Auto-undo when modules are removed
- [Stream-Edit](writing-modules/stream-edit.md) — Post-process file content through agent-local scripts
- [Config File Format](writing-modules/config-format.md) — Modules, roles, envoys, vars, environments.vgo
- [Usercrates](writing-modules/usercrates.md) — Per-user modules: directory layout, compliance, retirement

## Subsystems

Optional subsystems. Read the one you're using; skip the rest.

- [Swarm](subsystems/swarm.md) — The envoy P2P substrate (transport layer for payload, longdrawer, gitmirror, lockbox)
- [Spanner](subsystems/spanner.md) — Hub-spoke multi-server topology
- [P2P Spanner](subsystems/p2p-spanner.md) — Peer-to-peer spanner (design)
- [Scrier](subsystems/scrier.md) — Admin remote access (SSH/RDP/VNC) over the envoy's outbound tunnel
- [Gitmirror](subsystems/gitmirror.md) — Personal-DR git mirroring, P2P
- [Longdrawer](subsystems/longdrawer.md) — Per-user plaintext directory sync across LAN envoys
- [Lockbox](subsystems/lockbox.md) — Per-user encrypted P2P directory
- [Network Devices](subsystems/network-devices.md) — Gateway-proxy architecture for switches/routers

## Extending

For teams writing their own plugins.

- [Custom Executors](extending/custom-executors.md) — JSON stdin/stdout protocol
- [Custom Traits](extending/custom-traits.md) — Custom trait collector scripts
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
