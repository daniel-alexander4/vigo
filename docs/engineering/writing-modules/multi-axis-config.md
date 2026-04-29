
# Multi-Axis Configuration

Vigo composes an envoy's effective config from **a small number of orthogonal axes**. One canonical way to answer each question. The axis list is deliberately short — every mechanism earns its place by doing something the others can't.

The whole config operation lives under a single parent directory, **stacks** (`/srv/vigo/stacks/`). Inside it, three subtrees have distinct roles:

- **`stockpile/`** — the operator's working tree. Holds *every* config primitive: `modules/` (definitions), `usercrates/`, `templates/`, `tasks/`, `workflows/`, plus the assignment files themselves (`envoys.vgo` matchfiles, `roles.vgo`, `common.vgo`, `waivers.vgo`, `environments.vgo`) and directory-inherited `compliance.vgo` claims. **There is one operator-edited tree.**
- **`examples/`** — read-only reference templates seeded by the image. Operators copy from here into stockpile via `vigocli config examples copy <name>`. Never read by the server, never published.
- **`.live/`** — the validated published output. `vigocli config publish` lints stockpile and atomically syncs it here; the server reads `.live/` on every reload.

Operators edit `stockpile/`. Never `.live/`. Never edit `examples/` directly — copy first, then edit your stockpile copy.

> The 0.33.1 collapse merged the prior `worksite/{stockpile,scaffolding,structure}/` layout into the `stacks/{stockpile,examples,.live}/` shape. Existing installs migrate automatically on first boot. The `scaffolding/` tree is gone — its contents (matchfiles, roles, common.vgo, waivers, environments.vgo) live in stockpile alongside modules and usercrates.

## The axes

| Axis | Where | What it does |
|---|---|---|
| **Directory structure** | `stockpile/<org>/<site>/` | Organizes by business axis (customer, site, tenant). Inheritance via `common.vgo`. **Not used for env.** |
| **Matchfiles** | `stockpile/**/envoys.vgo` (or any `.vgo` with an `envoys:` block, anywhere outside `modules/`/`templates/`/`tasks/`/`workflows/`/`usercrates/`) | Maps hostname patterns to roles + environment + tags + vars. First-match-wins. |
| **Roles** | `stockpile/roles.vgo` | Named groupings of modules. Supports `when:` (role-wide) and `case:` (per-platform module groups). Single fleet-wide file at the stockpile root. |
| **Modules** | `stockpile/modules/**/*.vgo` | Reusable resource sets. Declared once, referenced by roles or `common.vgo`. Directory name is `modules/` on disk; YAML keys and identifiers stay `module`. |
| **Usercrates** | `stockpile/**/usercrates/*.vgo` | Per-user modules. Auto-apply within directory scope — no need to list in `common.vgo`. |
| **Common defaults** | `stockpile/**/common.vgo` | Directory-inherited modules/roles/vars + `exclude_modules`/`exclude_usercrates`. |
| **Environment** | `environment:` field on match block | Cross-cutting env declaration. Orthogonal to directory structure. |
| **Per-env overrides** | `stockpile/**/environments.vgo` | Env-specific module sets + vars. Scope follows ancestor chain. |
| **Lookup tables** | Inside module fields (`tag:...`, `os_family:...`) | Per-trait or per-tag field variation. |
| **Conditional resources** | `when:` on individual resources | Skip a resource based on traits or vars. |
| **Compliance claims** | `stockpile/**/compliance.vgo` | Directory-level claim inheritance for modules + usercrates. |
| **Waivers** | `stockpile/**/waivers.vgo` | Per-envoy compliance exceptions. |

### Canonical filename rule

Two file shapes are protected by name. Both live in stockpile:

| Filename | Top-level key |
|---|---|
| `compliance.vgo` | `compliance:` (directory-inherited claims) |
| `waivers.vgo` | `waivers:` (directory-inherited waivers) |

Mixing the keys inside a file (e.g., `compliance.vgo` carrying a `waivers:` block) is rejected at publish and reload with a move hint. The validator also rejects:

- Any `.vgo` file with a top-level `waivers:` block but not named `waivers.vgo` — the loader globs the exact filename, so a stray `corp-waivers.vgo` would silently not load. Rename to `waivers.vgo`.
- Any "claims-only" `.vgo` file (top-level `compliance:` with no `resources:`/`name:`/`vars:`) not named `compliance.vgo` — same trap. Rename to `compliance.vgo`. Module definitions carrying their own `compliance:` block are unaffected (the validator looks at presence of `resources:`/`name:` to distinguish modules from claim files).

## Resolution order — one diagram

For an envoy `web01.customerA.example.com` checking in:

```
┌──────────────────────────────────────────────────────────────────┐
│  Match block lookup (first-match-wins, all matchfiles walked)    │
│                                                                  │
│  stockpile/customerA/envoys.vgo:                                 │
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
│       stockpile/common.vgo              → [baseline, monitoring] │
│       stockpile/customerA/common.vgo    → [customerA-baseline]   │
│                                                                  │
│  2. Role modules (expand via roles.vgo; case: applied per OS)    │
│       web-server → [nginx, php, tls]                             │
│                                                                  │
│  3. Usercrates (auto-apply, ancestor chain)                      │
│       stockpile/usercrates/dan.vgo              → users/dan      │
│       stockpile/customerA/usercrates/alice.vgo  → users/alice    │
│                                                                  │
│  4. environments.vgo (ancestor chain, env == "prod")             │
│       stockpile/environments.vgo (prod)  → [audit-logging]       │
│       stockpile/customerA/environments.vgo (prod)                │
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
│  to each module in the list, walking up the directory ancestor   │
│  chain of each module's source file.                             │
└──────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────────┐
│  Attach waivers (from stockpile/**/waivers.vgo) to the envoy,    │
│  walking up the directory ancestor chain of the matchfile.       │
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
| How does a module get assigned to an envoy? | Via a **role** (defined in `stockpile/roles.vgo`) that the matchfile assigns, OR via inheritance from `stockpile/**/common.vgo`. Inline `modules:` on a match block is allowed but warns at >4 entries — that's the signal you should define a role. |
| How does a host get per-host config? | Tag the host in its match block; use tag-keyed lookup tables inside modules to vary field values. Separate match blocks only for genuinely bespoke hosts. |
| How does config differ per env? | Declare `environment:` on the match block. Declare env-specific modules/vars in `stockpile/**/environments.vgo` at the appropriate directory level. Env is **cross-cutting** — orthogonal to the directory tree. Do NOT nest subdirectories by env. |
| How does a module branch per OS / platform? | `case:` on the role (when whole modules differ per platform) OR platform-keyed lookup tables inside module fields (when only values differ). Never inline `when:` on a resource when the axis is platform — that's harder to read. |
| How do vars resolve? | 3 layers, low-to-high: module defaults < common.vgo inherited vars < envoy match-block vars < environments.vgo vars. |
| How is a user account configured? | A **usercrate** in `stockpile/usercrates/` (fleet-wide) or `stockpile/<scope>/usercrates/` (scoped). Auto-applies within the directory scope — no listing needed. See [User Management](usercrates.md). |
| Where do compliance claims live? | `stockpile/**/compliance.vgo` — directory-level, inherited by every module and usercrate in the subtree. Inline `compliance:` on a module definition for module-specific exceptions. |
| Where do waivers live? | `stockpile/**/waivers.vgo` — at any directory level, scoped by directory. Distinct filename from compliance claims. |
| Where do I copy an example from? | `vigocli config examples list` to browse, then `vigocli config examples copy <name>` to materialize one into stockpile. |

## Directory layout — reference

```
/srv/vigo/stacks/
├── stockpile/                    ← operator-edited working tree
│   ├── compliance.vgo            ← fleet-wide claims
│   ├── waivers.vgo               ← fleet-wide waivers
│   ├── common.vgo                ← fleet-wide defaults (modules, roles, vars)
│   ├── environments.vgo          ← fleet-wide env overrides (optional)
│   ├── roles.vgo                 ← role definitions (single, fleet-wide)
│   │
│   ├── modules/                  ← module definitions (any depth)
│   │   ├── nginx.vgo
│   │   ├── postgres.vgo
│   │   └── compliance.vgo        ← claims inherited by every module beneath
│   ├── usercrates/               ← fleet-wide users (auto-apply everywhere)
│   │   ├── dan.vgo
│   │   └── compliance.vgo        ← claims inherited by every usercrate here
│   ├── templates/                ← Go template files referenced by source: in modules
│   ├── tasks/                    ← reusable task definitions for `vigocli task dispatch`
│   ├── workflows/                ← workflow definitions
│   │
│   └── customerA/                ← organization axis: customer A
│       ├── common.vgo            ← customerA defaults
│       ├── waivers.vgo           ← customerA waivers (scoped)
│       ├── environments.vgo      ← customerA env overrides (optional)
│       ├── envoys.vgo            ← matchfile: hostname → role + env + tags
│       └── usercrates/
│           └── alice.vgo         ← customerA-only usercrate
│
├── examples/                     ← read-only, image-seeded reference templates
│   ├── modules/...               ← copy via `vigocli config examples copy <name>`
│   ├── usercrates/...
│   └── ...
│
└── .live/                        ← validated published output (server reads here;
                                    locked read-only between publishes)
    └── stockpile/                ← canonical bytes synced from stockpile after modlint
```

Key rules:

- **Operators only edit `stockpile/`.** `.live/` is locked between publishes; `examples/` is image-managed.
- **No env in directory paths.** No `prod/` or `staging/` directories. Env is a field on the match block.
- **Roles live once.** `stockpile/roles.vgo`, fleet-wide.
- **Modules live under `stockpile/modules/`.** Walked recursively.
- **Usercrates live at any scope within stockpile.** Root for fleet-wide, customer subdir for customer-scoped.
- **Organization axes** (customer, site, tenant, region, cloud) mirror your business, not your infrastructure. `aws/` or `gcp/` also works if cloud is your organizing axis.

## Layout history

These transitions all migrate automatically via the container entrypoint on first boot — no operator action required.

- **0.28.0** — moved the tree under `worksite/`; promoted `.live/` out as sibling `structure/`.
- **0.29.0** — split into `stockpile/` (module library) and `stockpile/` (envoy rules) subtrees; renamed `stockpile/modules/` at the filesystem level (YAML keys + Go/Rust identifiers keep `module`); renamed waivers-tree `compliance.vgo` → `waivers.vgo`.
- **0.29.1** — enforced filename-per-tree as fatal checks at publish and load (previously a silent warning).
- **0.33.1** — collapsed `worksite/{stockpile,scaffolding,structure}/` into `stacks/{stockpile,examples,.live}/`. Single operator-edited tree (stockpile) holding every primitive; `.live/` rename completes the path. The two-tree filename-per-tree rule simplified to canonical-filename rules within stockpile.

## What this replaces — removed 0.27.0

These mechanisms were deleted because they duplicated what other axes already do, not because their underlying use cases went away.

- **`environment_overrides:`** on match blocks (per-host in-matchfile env overrides). **Removed.** Use `environments.vgo` at the appropriate directory level.
- **`vars_from:`** external var files loaded into match blocks or `common.vgo`. **Removed.** Put shared var blocks in `common.vgo` at a parent directory — directory inheritance does what `vars_from:` did without the extra file concept.
- **Conditional vars** (`when:`-guarded var values that produced booleans). **Removed.** Use `when: "os_family('debian')"` directly on resources or in role `case:` — evaluate against traits without the indirection layer. If you need a named boolean shared across resources, define a tag on the envoy.
- **Match vars** (`when:`-with-fallback chains inside var definitions). **Removed.** Use tag-keyed or platform-keyed lookup tables inside the resource fields that need the variation.
- **`modules/users/` pattern** (per-user modules listed in `common.vgo`). **Removed.** Migrate to `stockpile/usercrates/` — auto-applies, no listing needed. See [User Management](usercrates.md).

Conditional vars and match vars flowed all the way to the agent via protobuf `ConditionalVarDef`. That machinery is gone; the agent no longer evaluates trait-based boolean vars on each check-in.

## Related reading

- [Config Format](config-format.md) — file-level YAML structure for modules, roles, matchfiles, common.vgo, environments.vgo.
- [User Management](usercrates.md) — the usercrate pattern for per-user config.
- [Lookup Tables](lookups.md) — tag-keyed and platform-keyed field variation inside modules.
- [Composition Patterns](composition-patterns.md) — roles, includes, inheritance, when expressions.
- [Glossary](../../reference/glossary.md) — canonical term definitions for stacks, stockpile, examples, .live, stone, usercrate.
