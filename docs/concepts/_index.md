# Concepts

Understanding-oriented. The mental model behind Vigo — how the pieces fit and why they're shaped the way they are. Read these when you want the *why*, not a step or a parameter. (For decisions and their rationale at the engineering level, see the [ADRs](../adr/); those are not operator docs.)

- [**Architecture**](architecture.md) — server, agent, CLI, and web UI; the check-in → resolve → apply → report loop.
- [**Configcrates**](configcrates.md) — the configuration model: configcrates, roles, hostcrates, usercrates, environments, and var precedence.
- [**Convergence vs compliance**](convergence-vs-compliance.md) — the two orthogonal axes Vigo never crosses.
- [**Secrets**](secrets.md) — the `secret:` convention, marker substitution, and pluggable backends.
- [**The swarm**](the-swarm.md) — the peer-to-peer substrate and its six content subsystems.
- [**Puddle identity**](puddle-identity.md) — the per-user identity every swarm subsystem builds on.
- [**Spanner federation**](spanner-federation.md) — peer-equal control-plane partitioning past one server.
- [**Scrier**](scrier.md) — reverse remote access with no inbound ports.
- [**The security model**](security-model.md) — mTLS, ed25519 signing, RBAC, and the audit hash chain.
- [**The compliance model**](compliance-model.md) — frameworks, capabilities, bundles, Connwaer, and waivers.
- [**High availability**](high-availability.md) — peer replicas, spanner, and durable state.

---
*Confidential — Alexander4, LLC. Not for redistribution.*
