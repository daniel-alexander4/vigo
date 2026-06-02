
# vigo (agent) reference

Reference for the agent binary that runs on every envoy and applies the desired-state policy you publish from the server.

| | |
|---|---|
| [resources/](resources/) | The 79 resource types you write configcrates against — files, packages, services, users, repositories, exec, and every OS-specific variant. One page per type. |
| [traits/](traits/) | The 54 trait collectors that produce the `.Traits` namespace your configcrates read (network, OS, hardware, cloud, packages, processes, cgroup pressure, …). One page per collector. |

The agent is a single Rust binary (`vigo`) that ships ~7 MB on x86_64 / ~6 MB on arm64, statically compiled with no runtime dependencies. It runs as a per-OS service (`vigo-envoy` on systemd Linux, plus launchd / rc.d / rcctl / SMF / Windows Service equivalents) and is multicall: the same binary also dispatches as `git-remote-gitback` when invoked under that name.

Operator-facing CLI surface on the agent: `vigo run`, `vigo status`, `vigo version`, and the `vigo swarm …` family for the swarm content subsystems (puddle, lockbox, gitback, longdrawer, curator). Most operator work goes through `vigocli` against the server; the agent's CLI is for offline introspection and emergency control.

---
*Confidential — Alexander4, LLC. Not for redistribution. See [../../legal/license.md](../../legal/license.md).*
