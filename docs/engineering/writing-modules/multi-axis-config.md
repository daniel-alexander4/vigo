
# Multi-Axis Configuration

Vigo composes an envoy's effective config from **a small number of orthogonal axes**. One canonical way to answer each question. The axis list is deliberately short — every mechanism earns its place by doing something the others can't.

## The axes

| Axis | Where | What it does |
|---|---|---|
| **Directory structure** | `stockpile/<org>/<site>/` | Organizes by business axis (customer, site, tenant). Inheritance via `common.vgo`. **Not used for env.** |
| **Matchfiles** | `envoys.vgo` (or any `.vgo` with an `envoys:` block) | Maps hostname patterns to roles + environment + tags + vars. First-match-wins. |
| **Roles** | `roles.vgo` at stockpile root | Named groupings of modules. Supports `when:` (role-wide) and `case:` (per-platform module groups). |
| **Modules** | `stockpile/modules/**/*.vgo` | Reusable resource sets. Declared once, referenced by roles or `common.vgo`. |
| **Usercrates** | `stockpile/**/usercrates/*.vgo` | Per-user modules. Auto-apply within directory scope — no need to list in `common.vgo`. |
| **Environment** | `environment:` field on match block | Cross-cutting env declaration. Orthogonal to directory structure. |
| **Per-env overrides** | `per_env_overrides.vgo` at any directory level | Env-specific module sets + vars. Scope follows ancestor chain. |
| **Lookup tables** | Inside module fields (`tag:...`, `os_family:...`) | Per-trait or per-tag field variation. |
| **Conditional resources** | `when:` on individual resources | Skip a resource based on traits or vars. |
| **Compliance claims** | `compliance.vgo` in modules tree | Directory-level claim inheritance. |
| **Waivers** | `compliance.vgo` in envoys tree | Per-envoy compliance exceptions. |

## Resolution order — one diagram

For an envoy `web01.customerA.example.com` checking in:

```
┌──────────────────────────────────────────────────────────────────┐
│  Match block lookup (first-match-wins, all matchfiles walked)    │
│                                                                  │
│  customerA/envoys.vgo:                                           │
│    - match: "web01.customerA.*"                                  │
│      role: web-server                                            │
│      environment: prod         ← env declared HERE               │
│      tags: [web, primary]      ← tags declared HERE              │
│      vars: { port: 443 }       ← envoy-level vars                │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────────┐
│  Compose module list (in order)                                  │
│                                                                  │
│  1. Inherited common.vgo modules (parent → child)                │
│       stockpile/common.vgo            → [baseline, monitoring]   │
│       stockpile/customerA/common.vgo  → [customerA-baseline]     │
│                                                                  │
│  2. Role modules (expand via roles.vgo; case: applied per OS)    │
│       web-server → [nginx, php, tls]                             │
│                                                                  │
│  3. Usercrates (auto-apply, ancestor chain)                      │
│       stockpile/usercrates/dan.vgo    → users/dan                │
│       stockpile/customerA/usercrates/alice.vgo → users/alice     │
│                                                                  │
│  4. per_env_overrides.vgo (ancestor chain, env == "prod")        │
│       stockpile/per_env_overrides.vgo (prod) → [audit-logging]   │
│       stockpile/customerA/per_env_overrides.vgo (prod)           │
│                                         → [customerA-prod-only]  │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────────┐
│  Apply exclusions                                                │
│    match block:  exclude_modules / exclude_usercrates            │
│    common.vgo:   exclude_usercrates (dir scope and below)        │
│    per_env:      exclude_modules / exclude_usercrates per env    │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────────┐
│  Final resolved module list                                      │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────────┐
│  Var resolution (3-layer, low → high)                            │
│    module defaults                                               │
│      < common.vgo inherited vars (parent → child)                │
│      < match-block vars                                          │
│      < per_env_overrides.vgo vars (env == "prod")                │
│                                                                  │
│  Tag-keyed and platform-keyed lookup tables inside modules       │
│  resolve final field values based on the envoy's tags + traits.  │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────────┐
│  Resource-level `when:` expressions filter the final resource    │
│  set per envoy. Trait-function calls evaluate agent-side; pure   │
│  var expressions evaluate server-side.                           │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼
                      Bundle sent to agent
```

## Canonical rulebook — one answer per question

| Question | Canonical answer |
|---|---|
| How does a module get assigned to an envoy? | Via a **role** (defined in `roles.vgo`) that the matchfile assigns, OR via inheritance from `common.vgo` at a parent directory. Inline `modules:` on a match block is allowed but warns at >4 entries — that's the signal you should define a role. |
| How does a host get per-host config? | Tag the host in its match block; use tag-keyed lookup tables inside modules to vary field values. Separate match blocks only for genuinely bespoke hosts. |
| How does config differ per env? | Declare `environment:` on the match block. Declare env-specific modules/vars in `per_env_overrides.vgo` at the appropriate directory level. Env is **cross-cutting** — orthogonal to the directory tree. Do NOT nest subdirectories by env. |
| How does a module branch per OS / platform? | `case:` on the role (when whole modules differ per platform) OR platform-keyed lookup tables inside module fields (when only values differ). Never inline `when:` on a resource when the axis is platform — that's harder to read. |
| How do vars resolve? | 3 layers, low-to-high: module defaults < common.vgo inherited vars < envoy match-block vars < per_env_overrides.vgo vars. |
| How is a user account configured? | A **usercrate** in `stockpile/usercrates/` (fleet-wide) or `stockpile/<scope>/usercrates/` (scoped). Auto-applies within the directory scope — no listing needed. See [User Management](user-management.md). |
| Where do compliance claims live? | Directory-level `compliance.vgo` in the modules tree, inherited by every module in the subtree. Inline `compliance:` only for module-specific exceptions. |
| Where do waivers live? | `compliance.vgo` in the envoys tree (any directory level), scoped by directory. |

## Directory layout — reference

```
stockpile/
├── roles.vgo                     ← role definitions (fleet-wide, ONE file)
├── common.vgo                    ← fleet-wide defaults (modules, roles, vars)
├── compliance.vgo                ← fleet-wide waivers (envoys tree)
├── per_env_overrides.vgo         ← fleet-wide env overrides (optional)
│
├── modules/                      ← shared module library
│   ├── nginx.vgo
│   ├── postgres.vgo
│   └── compliance.vgo            ← module-tree claim inheritance
│
├── usercrates/                   ← fleet-wide users (auto-apply everywhere)
│   └── dan.vgo
│
├── customerA/                    ← organization axis: customer A
│   ├── common.vgo                ← customerA defaults
│   ├── envoys.vgo                ← matchfile: hostname → role + env + tags
│   ├── per_env_overrides.vgo     ← customerA env overrides (optional)
│   └── usercrates/               ← customerA-only users
│       └── alice.vgo
│
└── customerB/
    ├── common.vgo
    ├── envoys.vgo
    └── usercrates/
        └── bob.vgo
```

Key rules:
- **No env in directory paths.** No `prod/` or `staging/` directories. Env is a field on the match block.
- **Roles live once.** `stockpile/roles.vgo`, fleet-wide.
- **Modules live once.** `stockpile/modules/*.vgo`, the shared library.
- **Usercrates live at any scope.** Root for fleet-wide, customer subdir for customer-scoped.
- **Organization axes** (customer, site, tenant, region, cloud) mirror your business, not your infrastructure. `aws/` or `gcp/` also works if cloud is your organizing axis.

## What this replaces — removed 0.27.0

These mechanisms were deleted because they duplicated what other axes already do, not because their underlying use cases went away.

- **`environment_overrides:`** on match blocks (per-host in-matchfile env overrides). **Removed.** Use `per_env_overrides.vgo` at the appropriate directory level.
- **`vars_from:`** external var files loaded into match blocks or `common.vgo`. **Removed.** Put shared var blocks in `common.vgo` at a parent directory — directory inheritance does what `vars_from:` did without the extra file concept.
- **Conditional vars** (`when:`-guarded var values that produced booleans). **Removed.** Use `when: "os_family('debian')"` directly on resources or in role `case:` — evaluate against traits without the indirection layer. If you need a named boolean shared across resources, define a tag on the envoy.
- **Match vars** (`when:`-with-fallback chains inside var definitions). **Removed.** Use tag-keyed or platform-keyed lookup tables inside the resource fields that need the variation.
- **`modules/users/` pattern** (per-user modules listed in `common.vgo`). **Removed.** Migrate to `stockpile/usercrates/` — auto-applies, no listing needed. See [User Management](user-management.md).

Conditional vars and match vars flowed all the way to the agent via protobuf `ConditionalVarDef`. That machinery is gone; the agent no longer evaluates trait-based boolean vars on each check-in.

## Related reading

- [Config Format](config-format.md) — file-level YAML structure for modules, roles, matchfiles, common.vgo, per_env_overrides.
- [User Management](user-management.md) — the usercrate pattern for per-user config.
- [Lookup Tables](lookups.md) — tag-keyed and platform-keyed field variation inside modules.
- [Composition Patterns](composition-patterns.md) — roles, includes, inheritance, when expressions.
- [Glossary](../reference/glossary.md) — canonical term definitions.
