<!--
Copyright (c) 2026 Alexander4, LLC
SPDX-License-Identifier: LicenseRef-Proprietary
-->

# ADR-036: Per-configcrate `state:` — configcrate-level retraction

**Status:** Accepted — implemented in 0.75.0.
**Date:** 2026-06-08

## Context

Retracting a whole configcrate today means the `.retract` workflow: `vigocli
config publish` AI-generates a `<name>.retract` file, the operator renames it,
adds it to a match block, publishes, waits for convergence, then removes it. For
the common case — reverse a configcrate built entirely from reversible resources
— that's a lot of manual file shuffling for what is conceptually one declarative
intent: "this configcrate should not be enforced on these envoys; undo it."

The primitives to express that already shipped: per-resource `state: absent` on
the ~77 stateful types, and `revert:`/`on_revert:` on the command types
(0.73.0/0.74.4). The agent already converges both. A `state:` field on a
configcrate **reference** can compose on top of them entirely server-side — no
agent change, no redeploy.

The hard part is precedence. A configcrate can be referenced from four carriers
(role, `common.vgo`, hostcrate match block, `environments.vgo`), and the
resolver flattens them into one list before deduping, losing carrier identity.
The naive "last-ref-wins" depends on the resolver's collection order being
ascending specificity — an implicit, fragile invariant that a future refactor
could silently invert.

## Decision

Add `State` (`present`|`absent`, default `present`) to `ConfigcrateRef`,
declarable on a reference in **any** of the four carriers. When a resolved
reference is `absent`, the loader transforms each of the configcrate's resources
to its retracted form in place during `resolveEntry`.

**Precedence is most-specific-wins**, resolved by comparing an explicit carrier
**tier**, not list position:

```
TierEnv (environments.vgo) > TierMatch (match block) > TierCommon (common.vgo) > TierRole (role-provided)
```

A finer carrier's explicit `state:` overrides a coarser one, so a coarse
retraction can be un-retracted at a finer level (retract fleet-wide in
`common.vgo`, set one host back `present` at its match block). Role-provided is
the least-specific tier: a direct declaration outranks a role's bundled content,
consistent with the existing var precedence.

The definition and `when:` stay first-wins (unchanged); `State` is the only axis
resolved by tier.

### Tiers tagged at the source, not in the flow

Every ref is stamped with its carrier tier **once, at load, in
`tagCarrierTiers`**, while it still sits in its typed source struct
(`RoleDefinition` → `TierRole`, `CommonConfig` → `TierCommon`, `EnvoyEntry` →
`TierMatch`, `PerEnvOverrideBlock` → `TierEnv`). Downstream flattening, dedup,
and `foreach` expansion carry the tag forward. This was chosen over tagging at
the ~13 points refs flow *through* the resolver: the source structs are 4
unambiguous spots, and tagging there covers the `Get()` root-`common.vgo`
fallback path (which bypasses `applyInheritance`) for free.

Because precedence compares tiers rather than list position, a future reordering
of the resolution pipeline cannot silently invert it. The zero value
`TierUnset` is invalid: a ref reaching resolution untagged means a tagging site
was missed — `resolveEntry` fails that entry loudly, and a unit test asserts
every resolved ref is tagged.

### Shared reversal SoT

The per-type "retracted form" decision (services → `stopped`+`disabled`, other
stateful types → `absent`, command types → `revert:`) lives in
`validate.RetractedStateAttrs`, the single source of truth. The inline transform
calls it directly; the existing `.retract` generator is pinned to it by a
conformance test (`TestRetractGeneratorMatchesStateSoT`) so the two can't drift.

### Validation

Publish-time errors, each naming the offender: a non-reversible resource type in
an `absent` configcrate; a command resource lacking `on_revert:`; `state:` on a
usercrate reference (usercrate retraction = user removal, out of scope); an
invalid `state:` value.

## Consequences

- Declarative, version-controlled, per-carrier retraction that flips with the
  config — no manual `.retract` file dance for the common case.
- Server-side only. The agent already converges `state: absent` and `revert:`;
  no agent change or redeploy.
- Coexists with (does not supersede) the AI `.retract` workflow — the inline
  flag covers declared-reversible resources; `.retract` handles arbitrary
  resources the AI must reason about.
- **Reach is bounded by reversal coverage.** A configcrate containing a
  not-yet-reversible type (cisco/junos stanza setters, Windows-only policy
  setters — the tracked reversal follow-on) can't be inline-retracted and errors
  legibly at publish. Limited until that work lands; `.retract` remains the
  escape hatch.
- Reverse-DAG teardown is out of scope — idempotent forward-DAG convergence
  suffices for retraction.

## Alternatives considered

- **Monotonic absent-wins** (any carrier's `absent` retracts, no un-retract).
  Simpler (no tier comparison) and consistent with `exclude_configcrates`'s
  monotonic model, but it can't un-retract at a finer level — rejected because
  the four-carrier design's value is precisely per-level override.
- **Positional last-wins** (no tiers, rely on collection order). Rejected: the
  ascending-specificity invariant is implicit and a refactor would invert
  precedence silently.
- **Fold into the `.retract` generator.** Rejected: that generator renames
  resources, reorders, and emits AI-review placeholders — a file-generation
  shape wrong for an in-place transform. Only the per-type state mapping is
  genuinely shared, and that's extracted to `validate`.
