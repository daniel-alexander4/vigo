
# Multi-Axis Configuration

Vigo composes an envoy's effective config from **a small number of orthogonal axes**. One canonical way to answer each question. The axis list is deliberately short — every mechanism earns its place by doing something the others can't.

The whole config operation lives under a single parent directory, the **worksite** (`/srv/vigo/worksite/`). Inside it, three subtrees have distinct roles:

- **`stockpile/`** — the module library (parts depot). Holds `stones/` (module definitions), `usercrates/`, `templates/`, and `compliance.vgo` directory-inherited claims. Nothing in here references specific envoys.
- **`scaffolding/`** — the envoy-assignment tree (orders). Holds `envoys.vgo` matchfiles, `roles.vgo`, `common.vgo` inheritance, `waivers.vgo`, `environments.vgo`, and customer/site subdirs.
- **`structure/`** — the validated published output. `vigocli config publish` syncs stockpile + scaffolding here after modlint; the server reads from this directory.

Operators edit `stockpile/` and `scaffolding/`. Never `structure/`.

## The axes

| Axis | Where | What it does |
|---|---|---|
| **Directory structure** | `scaffolding/<org>/<site>/` | Organizes by business axis (customer, site, tenant). Inheritance via `common.vgo`. **Not used for env.** |
| **Matchfiles** | `scaffolding/**/envoys.vgo` (or any `.vgo` with an `envoys:` block in scaffolding) | Maps hostname patterns to roles + environment + tags + vars. First-match-wins. |
| **Roles** | `scaffolding/roles.vgo` | Named groupings of modules. Supports `when:` (role-wide) and `case:` (per-platform module groups). |
| **Modules** | `stockpile/stones/**/*.vgo` | Reusable resource sets. Declared once, referenced by roles or `common.vgo`. Directory name is `stones/` on disk; YAML keys and identifiers stay `module`. |
| **Usercrates** | `stockpile/**/usercrates/*.vgo` | Per-user modules. Auto-apply within directory scope — no need to list in `common.vgo`. |
| **Environment** | `environment:` field on match block | Cross-cutting env declaration. Orthogonal to directory structure. |
| **Per-env overrides** | `scaffolding/**/environments.vgo` | Env-specific module sets + vars. Scope follows ancestor chain. |
| **Lookup tables** | Inside module fields (`tag:...`, `os_family:...`) | Per-trait or per-tag field variation. |
| **Conditional resources** | `when:` on individual resources | Skip a resource based on traits or vars. |
| **Compliance claims** | `stockpile/**/compliance.vgo` | Directory-level claim inheritance for modules + usercrates. |
| **Waivers** | `scaffolding/**/waivers.vgo` | Per-envoy compliance exceptions. |

### Filename-per-tree rule

Claims and waivers live in **different filenames in different trees** — enforced fatally at publish and load (phase 3, 0.29.1). The old "same filename, location determines meaning" rule is gone.

| Filename | Tree | Top-level key |
|---|---|---|
| `compliance.vgo` | `stockpile/` | `compliance:` (claims) |
| `waivers.vgo` | `scaffolding/` | `waivers:` (waivers) |

Putting `waivers.vgo` in stockpile, `compliance.vgo` in scaffolding, or mixing the keys inside a file is rejected with a move hint. No silent warnings.

## Resolution order — one diagram

For an envoy `web01.customerA.example.com` checking in:

```
┌──────────────────────────────────────────────────────────────────┐
│  Match block lookup (first-match-wins, all matchfiles walked)    │
│                                                                  │
│  scaffolding/customerA/envoys.vgo:                               │
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
│       scaffolding/common.vgo              → [baseline, monitoring] │
│       scaffolding/customerA/common.vgo    → [customerA-baseline]   │
│                                                                  │
│  2. Role modules (expand via roles.vgo; case: applied per OS)    │
│       web-server → [nginx, php, tls]                             │
│                                                                  │
│  3. Usercrates (auto-apply, ancestor chain; stockpile tree)      │
│       stockpile/usercrates/dan.vgo              → users/dan      │
│       stockpile/customerA/usercrates/alice.vgo  → users/alice    │
│                                                                  │
│  4. environments.vgo (ancestor chain, env == "prod")             │
│       scaffolding/environments.vgo (prod)  → [audit-logging]     │
│       scaffolding/customerA/environments.vgo (prod)              │
│                                            → [customerA-prod-only] │
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
│  Attach compliance claims (from stockpile/**/compliance.vgo)     │
│  to each module in the list, walking up the stockpile-tree       │
│  ancestor chain of each module's source file.                    │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────────┐
│  Attach waivers (from scaffolding/**/waivers.vgo) to the envoy,  │
│  walking up the scaffolding ancestor chain of the matchfile.     │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────────┐
│  Var resolution (3-layer, low → high)                            │
│    module defaults                                               │
│      < common.vgo inherited vars (parent → child)                │
│      < match-block vars                                          │
│      < environments.vgo vars (env == "prod")                     │
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
| How does a module get assigned to an envoy? | Via a **role** (defined in `scaffolding/roles.vgo`) that the matchfile assigns, OR via inheritance from `scaffolding/**/common.vgo`. Inline `modules:` on a match block is allowed but warns at >4 entries — that's the signal you should define a role. |
| How does a host get per-host config? | Tag the host in its match block; use tag-keyed lookup tables inside modules to vary field values. Separate match blocks only for genuinely bespoke hosts. |
| How does config differ per env? | Declare `environment:` on the match block. Declare env-specific modules/vars in `scaffolding/**/environments.vgo` at the appropriate directory level. Env is **cross-cutting** — orthogonal to the directory tree. Do NOT nest subdirectories by env. |
| How does a module branch per OS / platform? | `case:` on the role (when whole modules differ per platform) OR platform-keyed lookup tables inside module fields (when only values differ). Never inline `when:` on a resource when the axis is platform — that's harder to read. |
| How do vars resolve? | 3 layers, low-to-high: module defaults < common.vgo inherited vars < envoy match-block vars < environments.vgo vars. |
| How is a user account configured? | A **usercrate** in `stockpile/usercrates/` (fleet-wide) or `stockpile/<scope>/usercrates/` (scoped). Auto-applies within the directory scope — no listing needed. See [User Management](usercrates.md). |
| Where do compliance claims live? | `stockpile/**/compliance.vgo` — directory-level in the stockpile tree, inherited by every module and usercrate in the subtree. Inline `compliance:` on a module definition for module-specific exceptions. |
| Where do waivers live? | `scaffolding/**/waivers.vgo` — at any scaffolding directory level, scoped by directory. Distinct filename from compliance claims. |

## Directory layout — reference

```
/srv/vigo/worksite/
├── stockpile/                    ← module library (parts)
│   ├── compliance.vgo            ← fleet-wide claims
│   ├── stones/                   ← module definitions (directory named "stones"; YAML keys stay "module")
│   │   ├── nginx.vgo
│   │   ├── postgres.vgo
│   │   └── compliance.vgo        ← claims inherited by every stone beneath
│   ├── usercrates/               ← fleet-wide users (auto-apply everywhere)
│   │   ├── dan.vgo
│   │   └── compliance.vgo        ← claims inherited by every usercrate here
│   ├── templates/                ← Go template files referenced by source: in modules
│   └── customerA/
│       └── usercrates/
│           └── alice.vgo         ← customerA-only usercrate
│
├── scaffolding/                  ← envoy-assignment tree (orders)
│   ├── roles.vgo                 ← role definitions (fleet-wide, ONE file)
│   ├── common.vgo                ← fleet-wide defaults (modules, roles, vars)
│   ├── waivers.vgo               ← fleet-wide compliance waivers
│   ├── environments.vgo          ← fleet-wide env overrides (optional)
│   ├── customerA/                ← organization axis: customer A
│   │   ├── common.vgo            ← customerA defaults
│   │   ├── envoys.vgo            ← matchfile: hostname → role + env + tags
│   │   ├── waivers.vgo           ← customerA waivers (scoped)
│   │   └── environments.vgo      ← customerA env overrides (optional)
│   └── customerB/
│       ├── common.vgo
│       └── envoys.vgo
│
└── structure/                    ← validated published output (server reads here)
    ├── stockpile/                ← copy of source stockpile after modlint
    └── scaffolding/              ← copy of source scaffolding after modlint
```

Key rules:

- **Nothing in stockpile talks about envoys.** Stockpile is the parts library. No matchfiles, no roles, no common.vgo, no waivers.
- **Nothing in scaffolding is a module definition.** Scaffolding is the assignment tree. No stones, no usercrates, no templates.
- **No env in directory paths.** No `prod/` or `staging/` directories. Env is a field on the match block.
- **Roles live once.** `scaffolding/roles.vgo`, fleet-wide.
- **Modules live once.** `stockpile/stones/*.vgo`, the shared library.
- **Usercrates live at any scope within stockpile.** Root for fleet-wide, customer subdir for customer-scoped.
- **Organization axes** (customer, site, tenant, region, cloud) mirror your business, not your infrastructure. `aws/` or `gcp/` also works if cloud is your organizing axis. These subdirs live under `scaffolding/` (for matchfiles) and/or `stockpile/` (for customer-specific usercrates).

## What this replaces — removed 0.27.0

These mechanisms were deleted because they duplicated what other axes already do, not because their underlying use cases went away.

- **`environment_overrides:`** on match blocks (per-host in-matchfile env overrides). **Removed.** Use `environments.vgo` at the appropriate scaffolding directory level.
- **`vars_from:`** external var files loaded into match blocks or `common.vgo`. **Removed.** Put shared var blocks in `common.vgo` at a parent directory — directory inheritance does what `vars_from:` did without the extra file concept.
- **Conditional vars** (`when:`-guarded var values that produced booleans). **Removed.** Use `when: "os_family('debian')"` directly on resources or in role `case:` — evaluate against traits without the indirection layer. If you need a named boolean shared across resources, define a tag on the envoy.
- **Match vars** (`when:`-with-fallback chains inside var definitions). **Removed.** Use tag-keyed or platform-keyed lookup tables inside the resource fields that need the variation.
- **`modules/users/` pattern** (per-user modules listed in `common.vgo`). **Removed.** Migrate to `stockpile/usercrates/` — auto-applies, no listing needed. See [User Management](usercrates.md).

Conditional vars and match vars flowed all the way to the agent via protobuf `ConditionalVarDef`. That machinery is gone; the agent no longer evaluates trait-based boolean vars on each check-in.

## What this replaces — restructured 0.28.0–0.29.1

The single flat `stockpile/` directory (which held modules, envoys.vgo, roles.vgo, common.vgo, compliance.vgo-for-waivers, usercrates, templates all together, plus a nested `.live/`) was split over three releases:

- **0.28.0** — moved the tree under `worksite/`, promoted `.live/` out as sibling `structure/`.
- **0.29.0** — split into `stockpile/` (module library) and `scaffolding/` (envoy rules) subtrees; renamed `stockpile/modules/` → `stockpile/stones/` at the filesystem level (YAML keys + Go/Rust identifiers keep `module`); renamed waivers-tree `compliance.vgo` → `waivers.vgo`.
- **0.29.1** — enforced filename-per-tree as fatal checks at publish and load (previously a silent warning).

Migration is automatic on first boot via `entrypoint.sh` — no operator action needed on existing deployments.

## Related reading

- [Config Format](config-format.md) — file-level YAML structure for modules, roles, matchfiles, common.vgo, environments.vgo.
- [User Management](usercrates.md) — the usercrate pattern for per-user config.
- [Lookup Tables](lookups.md) — tag-keyed and platform-keyed field variation inside modules.
- [Composition Patterns](composition-patterns.md) — roles, includes, inheritance, when expressions.
- [Glossary](../../reference/glossary.md) — canonical term definitions for worksite, stockpile, scaffolding, structure, stone, usercrate.
