# ADR-010: Convergence vs compliance terminology

**Status:** Accepted  
**Date:** 2025-06-01 (retroactive)

## Context

Vigo tracks two distinct aspects of node health: whether the agent has successfully applied its assigned modules, and whether those modules satisfy regulatory framework controls. These concepts were at risk of being conflated in code, UI, and documentation.

## Decision

Convergence and compliance are formally distinct terms with separate code paths, database tables, and UI pages. They must never be crossed.

- **Convergence** — module convergence. Whether an envoy's assigned modules have been applied successfully. Statuses: converged, relapsed, diverged, degraded, failed, no data. "Degraded" means some modules failed but not all (partial run). Package: `server/convergence/`. Table: `convergence_history`.
- **Compliance** — standards compliance. Whether an envoy's modules satisfy controls in regulatory frameworks (HIPAA, SOC 2, PCI DSS, etc.). Measured as coverage percentage per framework. Package: `server/config/` (ComplianceIndex).

## Consequences

- A node can be fully converged but non-compliant (modules applied, but they don't cover all required controls)
- A node can be compliant but not converged (modules are assigned that would satisfy controls, but they haven't been applied yet)
- UI, API, and CLI use the correct term for the correct concept — no ambiguity
- Reachability (online/offline) is tracked separately from both
- Trade-off: operators must learn two concepts instead of one "health" metric. This is intentional — collapsing them would hide real problems.

## Alternatives considered

- **Single "health" score** — rejected because it hides whether the problem is "modules aren't applying" vs "modules don't cover the required controls"
- **Compliance as a subset of convergence** — rejected because compliance is about what modules are assigned (a config question), not whether they've been applied (a runtime question)
