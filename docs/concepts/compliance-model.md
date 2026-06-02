# The compliance model

**How does Vigo decide a node "meets HIPAA"?** It doesn't guess and it doesn't take your word — a control is covered only when something concrete proves it. This page is the mental model; the per-control map is the [compliance matrix](../reference/compliance-matrix.md).

## Two kinds of compliance, never crossed

- **Envoy compliance** — does a *node's configcrates* satisfy controls in a regulatory framework (HIPAA, SOC 2, PCI DSS, ISO 27001, …)? Reported as coverage % per framework.
- **Vigo product compliance** — what the *server and agent* satisfy by construction (mTLS, RBAC, the audit hash chain, secret encryption). Surfaced on `/platform/compliance`. It never inflates envoy or fleet scoring.

And neither of those is [convergence](convergence-vs-compliance.md) — read that page if the words still feel slippery.

## When a control counts as covered

For a given envoy and framework control, coverage requires one of:

1. **A configcrate claims it.** Two curated catalogs route the claim:
   - **`provides:`** — cross-framework *functional capabilities* (`time-sync`, `audit-trail`); the catalog cross-walks each to the controls it satisfies.
   - **`bundle:`** — *framework-scope-cut* implementations.
   - or a directory-level `compliance.vgo` inheritance file. The loader unions all three sources.
2. **Connwaer proves it.** The standalone [Connwaer](../reference/vigo/traits/connwaer.md) agent runs *active* checks for things enforcement can't demonstrate (WORM, RNG validation, encryption at rest, segmentation) and reports findings as traits that escalate a control to *satisfied*.
3. **A waiver applies** — an explicit, recorded exception.

## Honest claims

Vigo reports what it can prove and refuses what it can't. It states clearly what it **enforces** vs **monitors** vs **requires external validation for**, and it doesn't claim controls outside its scope (long-term WORM archival, sub-millisecond kill switches). Coverage you can't substantiate isn't coverage.

## Pushing evidence out

Per-control pass/fail evidence pushes to GRC platforms (Vanta, Drata, Secureframe, ServiceNow GRC, …) on a schedule, and compliance documents export as PDFs for auditors and cyber-insurance.

## Where this shows up

- [Compliance reporting](../howto/compliance/compliance-reporting.md) — generate + export.
- [Compliance matrix](../reference/compliance-matrix.md) — the per-control reference.
- [`vigocli compliance`](../reference/vigocli/compliance.md).

---
*Confidential — Alexander4, LLC. Not for redistribution.*
