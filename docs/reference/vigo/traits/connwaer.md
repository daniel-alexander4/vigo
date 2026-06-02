---
title: connwaer
---

# connwaer

Surfaces the results of the standalone **connwaer** compliance-verification agent. Connwaer runs active checks for controls the main agent can't prove through enforcement alone (WORM, RNG validation, encryption at rest, network segmentation, and others), writes each capability's outcome to a JSON file, and this collector reads them and reports them under the `connwaer` trait namespace. The server consumes these results when scoring envoy compliance — a connwaer check can escalate a control to *satisfied*.

Connwaer is independently versioned and deployed; this collector only reads what it writes. Results directory:

- Linux: `/var/lib/connwaer/results/`
- Windows: `C:\ProgramData\Connwaer\results\`

Each `<capability>.json` file is one capability's result, parsed and keyed by filename under the namespace — e.g. `.Traits.connwaer.app_allowlisting.status`. Results older than **24 hours** are treated as stale and excluded (a connwaer that stops running stops reporting, rather than reporting forever-stale data). `.tmp` files (partial writes) are skipped.

## Shape

The namespace is a **map of capability → result object** — there is no fixed top-level envelope. Each value is the verbatim `CapabilityResult` connwaer wrote; the exact fields depend on the connwaer release, but every result carries at least a `status`. Reference a specific capability's field as `.Traits.connwaer.<capability>.<field>` in templates and `when:` conditions.

```jsonc
{
  "app_allowlisting":  { "status": "pass",  ... },
  "encryption_at_rest": { "status": "fail", ... }
}
```

## When the trait is absent

`null` when the results directory doesn't exist (connwaer not installed), can't be read, or holds no fresh (≤ 24 h) result files.

## See also

- [Compliance matrix](../../compliance-matrix.md) — how connwaer results fold into framework coverage.
- [`security_scan`](security_scan.md) — the main agent's own passive security trait.
