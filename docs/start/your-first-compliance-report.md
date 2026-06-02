# 4 · Your first compliance report

By the end of this page you'll have read your fleet's coverage against a regulatory framework and exported a report. Five minutes — and it closes the loop from *enforcement* to *evidence*.

## Prerequisites

- [Tutorial 3](your-first-configcrate.md) done — at least one converging envoy with a configcrate bound.

## 1. See where you stand

Open the **Compliance** page in the web UI. Pick a framework (HIPAA, SOC 2, PCI DSS, …). You'll see a **coverage percentage** and a per-control breakdown: which controls are covered, and *how* each one is covered — by a configcrate's claim, a [Connwaer](../concepts/compliance-model.md) check, or a waiver.

Your number will be low right now — you've only written one configcrate. That's the point: coverage reflects what you can actually **prove**, not what you hope is true. See [convergence vs compliance](../concepts/convergence-vs-compliance.md) for why these are different questions.

## 2. Make a control go green

Pick an uncovered control that a configcrate could satisfy, and claim it. The cleanest way is a `provides:` tag on a configcrate — Vigo's catalog cross-walks one capability tag to the controls it satisfies across every framework:

```yaml
configcrate: ntp
compliance:
  provides: [time-sync]
resources:
  - type: service
    name: chrony
    state: running
    enabled: true
```

`vigocli config publish`, wait an interval, and refresh the Compliance page — the `time-sync`-related controls flip to covered, and your percentage ticks up. You just turned enforcement into compliance evidence.

## 3. Export it

Generate a report for an auditor:

```bash
vigocli compliance report --framework hipaa        # human-readable / PDF
vigocli compliance export --format oscal           # machine-readable for GRC tools
```

Reports state plainly what Vigo **enforces** vs **monitors** vs **requires external validation for** — honest claims, not inflated ones.

## You're done

You've stood up a server, enrolled envoys, written and converged a configcrate, watched drift get corrected, and turned that enforcement into framework coverage you can hand an auditor. That's the full arc.

## Where to go next

- **[Concepts](../concepts/_index.md)** — the mental model behind what you just did.
- **[How-to guides](../howto/_index.md)** — the swarm subsystems, auth, monitoring, backup/DR, migrating from another CM.
- **[Reference](../reference/_index.md)** — every command, resource, trait, and config key.

---
*Confidential — Alexander4, LLC. Not for redistribution.*
