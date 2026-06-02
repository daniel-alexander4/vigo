# 3 · Your first configcrate

By the end of this page you'll have written a tiny [configcrate](../concepts/configcrates.md), published it, and watched an envoy converge to it on the dashboard. Ten minutes — this is the core Vigo loop.

## Prerequisites

- [Tutorial 2](enroll-your-first-envoy.md) done — at least one envoy checking in.
- Shell access to the server's stockpile (`/srv/vigo/stacks/stockpile/`).

## 1. Write a configcrate

A configcrate is a named set of idempotent resources. Create `configcrates/motd.vgo` under the stockpile:

```yaml
configcrate: motd
resources:
  - type: file
    target_path: /etc/motd
    content: |
      Managed by Vigo. Do not edit by hand.
    mode: "0644"
```

That's a complete configcrate — one `file` resource, idempotent (Vigo checks the content hash before writing).

## 2. Bind it to an envoy

A configcrate is inert until something includes it. Add a match block (conventionally in `envoys.vgo`) mapping a hostname pattern to it:

```yaml
envoys:
  - match: "*"
    configcrates: [motd]
```

(`*` targets every envoy — fine for a first run; real fleets scope by pattern, **first match wins**.)

## 3. Publish

Validate and push it live:

```bash
vigocli config publish
```

Publish lint-checks every `.vgo`, syncs to `.live/`, and triggers a server reload. A clean run reports the files it published; an invalid file is rejected here, before it reaches any envoy.

## 4. Watch it converge

This is the payoff. Within a check-in interval, the agent pulls the new bundle, applies the `file` resource, and reports back. On the dashboard the envoy's **convergence status shows the run** — one resource changed, then steady. Confirm on the node:

```bash
cat /etc/motd      # → "Managed by Vigo. Do not edit by hand."
```

Now edit `/etc/motd` by hand on the node and wait one interval: Vigo notices the drift and **puts it back**. That's enforcement — it converges and *stays* there.

## Checkpoint

You've written, bound, published, and converged a configcrate, and seen drift corrected. That's the whole model; everything else is more resources, smarter targeting, and composition.

**Next:** [Your first compliance report →](your-first-compliance-report.md)

See also: [Write your first configcrate](../howto/write-your-first-module.md) and the [configcrate authoring reference](../reference/writing-configcrates.md) for the full surface (conditionals, templates, roles, environments).

---
*Confidential — Alexander4, LLC. Not for redistribution.*
