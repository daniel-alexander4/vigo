# Concepts

Understanding-oriented. The mental model behind Vigo — how the pieces fit and why they're shaped the way they are. Read these when you want the *why*, not a step or a parameter. (For decisions and their rationale at the engineering level, see the [ADRs](../adr/); those are not operator docs.)

- **Architecture** — server, agent, CLI, and web UI; the check-in → resolve → apply → report loop.
- **Configcrates** — the configuration model: configcrates, roles, hostcrates, usercrates, environments, and var precedence.
- **Convergence vs compliance** — the two orthogonal axes Vigo never crosses.
- **Secrets** — the `secret:` convention, marker substitution, and pluggable backends.
- **The swarm** — the peer-to-peer substrate and its six content subsystems.
- **Puddle identity** — the per-user identity every swarm subsystem builds on.
- **Spanner federation** — peer-equal control-plane partitioning past one server.
- **Scrier** — reverse remote access with no inbound ports.
- **The security model** — mTLS, ed25519 signing, RBAC, and the audit hash chain.
- **The compliance model** — frameworks, capabilities, bundles, Connwaer, and waivers.
- **High availability** — peer replicas, spanner, and durable state.

> These explanation pages are being authored as part of the documentation rebuild.

---
*Confidential — Alexander4, LLC. Not for redistribution.*
