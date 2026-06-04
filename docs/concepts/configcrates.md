# Configcrates

**How do you tell Vigo what a machine should look like?**

You write **configcrates** — reusable sets of resources — and bind them to machines through a small set of composition primitives. A configcrate is the unit of reuse; everything else (roles, hostcrates, environments) is about *which* configcrates land on *which* envoys, with *what* variables.

## The primitives

| Primitive | What it is |
|---|---|
| **Configcrate** | A named set of [resources](../reference/vigo/resources/_index.md) (`file`, `package`, `service`, …) with default variables. Lives in `stacks/configcrates/*.vgo`. Inert until something includes it. |
| **Role** | A named group of configcrates (with per-OS `case:` groups and single-level `includes:`). Defined once in `roles.vgo`. |
| **Hostcrate** | A `.vgo` file mapping hostname patterns/globs → roles, configcrates, and vars. **First match wins.** |
| **Usercrate** | A per-Unix-user configcrate kind; positively included via a carrier's `usercrates:` field. |
| **Environment** | `environments.vgo` overrides configcrates / vars per environment name (dev / staging / prod). |
| **`common.vgo`** | At any directory level, defines configcrates/vars inherited by subdirectories — parent before child. |

A resource declares a `type` and must be **idempotent** — check before act. `depends_on` builds a DAG (cycles rejected at load); `notify` / `subscribes` re-apply a resource when a dependency changes.

## Variable precedence

When the same variable is set in several places, the most specific wins:

```
environments.vgo  >  match-block vars  >  inherited vars  >  configcrate defaults
```

## Conditionals & templates

- **`when:`** expressions gate a resource or configcrate on boolean logic over trait builtins — `os_family('debian')`, `!is_container && has_enough_ram`. Evaluated server-side where possible, else passed to the agent.
- **Templates** use Go template syntax with `.Traits` and `.Vars`, in `content:` attributes or external `source: templates/file.tmpl` files.
- **`foreach:`** stamps out a resource — or a whole configcrate — once per list item.

## The one rule that matters

**Configcrates must never need manual intervention to recover.** Use age-based `onlyif` guards, not static file checks; a configcrate that can wedge in a failure loop is broken. Vigo converges toward desired state on every interval and *stays* there — see [convergence vs compliance](convergence-vs-compliance.md).

## Where this shows up

- [Write your first configcrate](../howto/configcrates/write-your-first-configcrate.md) — hands-on.
- [Writing configcrates](../reference/configcrate-language.md) — the full authoring reference.
- [Publish config](../howto/configcrates/publish-config.md) — validate + push to the fleet.

---
*Confidential — Alexander4, LLC. Not for redistribution.*
