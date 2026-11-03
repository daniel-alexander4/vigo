# Convergence vs compliance

**Two words that sound similar and mean completely different things in Vigo.** Vigo never crosses them, and neither should you.

## The short answer

- **Convergence** answers: *is this envoy in the state my configcrates declare?* It's about enforcement — did the resources apply, and is anything drifting?
- **Compliance** answers: *does this envoy satisfy a regulatory framework?* It's about coverage — how much of HIPAA / SOC 2 / PCI is provably met?

An envoy can be perfectly **converged** (every configcrate applied cleanly) and still **non-compliant** (no one wrote a configcrate that covers the encryption-at-rest control). The reverse happens too.

## Convergence — two orthogonal axes plus reachability

Owned by `server/convergence/` and `server/freshness/`:

- **Failure** (`Status`): **Converged** / **Degraded** / **Failed** / **NoData** — mutually exclusive, sums to the fleet.
- **Drift** (`DriftLevel`): **None** / **Changed** / **Diverged** — independent of failure; N consecutive runs with changes flips an envoy to *Diverged*.
- **Reachability** — is the envoy even checking in? A per-envoy staleness predicate (`2.5 ×` its observed cadence), separate from both axes.

These are independent: a converged envoy can be drifting; a reachable envoy can be failed.

## Compliance — coverage against frameworks

A control counts as **covered** for an envoy when one of these holds:

1. a configcrate it has *claims* the control — via `provides:` (functional capabilities) or `bundle:` (framework-scope cuts), or a directory-level `compliance.vgo` file;
2. a [Connwaer](compliance-model.md) active check escalates it to *satisfied*; or
3. a waiver applies.

Coverage is a percentage per framework. See [the compliance model](compliance-model.md).

## Why keep them apart

Conflating them produces dishonest reporting — "100% converged" is not "100% compliant", and selling one as the other is how compliance theater happens. Vigo reports what it **enforces** (convergence) separately from what it **proves against a standard** (compliance), and never inflates one with the other.

## Where this shows up

- The dashboard's convergence + compliance cards (separate, drill down separately).
- [Compliance reporting](../howto/compliance/compliance-reporting.md) — generate framework coverage.
- [Configcrates](configcrates.md) — what convergence enforces.
