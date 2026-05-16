# Multi-Axis Module Application — Audit and Proposed Shape

Goal: identify the redundant / overlapping / trap-laden combinations in Vigo's config system today, propose a clean canonical shape that preserves real flexibility while removing "every sysadmin invents their own way" surface area.

Status: audit + proposal, no code changes. Decisions deferred to Dan.

Source of pending item: `~/.claude/projects/-home-dan-cairn-vigo/memory/pending.md` → "Examine multi-axis module application for simplification."

## Current axes

The set of mechanisms that can compose into a single resolved module list + var set for an envoy at check-in:

1. **Matchfiles** — `envoys:` blocks with hostname globs, first-match-wins. A match block can specify 12+ fields: `modules`, `usercrates`, `roles`, `vars`, `vars_from`, `exclude_modules`, `exclude_resources`, `tags`, `environment`, `environment_overrides`, `observe_only`, `match`, `node_type`.
2. **Directory inheritance via `common.vgo`** — modules/usercrates/roles/vars declared at a parent dir are inherited by every subdirectory; `exclude_modules:` for opt-out.
3. **Roles** — defined in `roles.vgo` (or legacy `roles/`), supporting `when:` (role-wide conditional), `case:` (per-platform module groups), `includes:` (single-level role composition).
4. **Per-environment overrides** — `environments.vgo` layered selection by env name.
5. **Vars merging** — seven layers: module defaults < inherited common.vgo < envoy match-block < environment_overrides in match block < environments.vgo < conditional vars (`when:`-guarded) < match vars (`when:`-with-fallback).
6. **vars_from:** — external var files loaded into match-block or common.vgo.
7. **Conditional resources** — `when:` expressions on individual resources, evaluated server-side where possible, agent-side for trait-dependent predicates.
8. **Usercrates** — user-scoped module kind. The `stockpile/**/usercrates/` directory is a library; activation requires a positive `usercrates:` include on a carrier (same 4 carriers `modules:` lives on). Auto-apply was retired in 0.54.0.
9. **Tag-based lookups** — per-tag/host/trait-keyed maps inside module fields (`shell: { tag:workstation: /bin/fish, default: /bin/bash }`).
10. **Platform-keyed lookups** — same mechanism as tag lookups but keyed on `os_family:linux`, `distribution:debian`, etc.
11. **Compliance attachment** — `compliance:` block inside a module OR `compliance.vgo` at a parent directory in the modules tree (claims merged into every module below).

## The combinatorial traps

### A. Module assignment — 5 independent paths

To get module `nginx` applied to envoy `web01`, an operator can (in combination):
- List it in a match block's `modules: [nginx]`
- Assign a role that references it (`web-server` role → `modules: [nginx, php, tls]`)
- Inherit it from `common.vgo` at any ancestor directory
- Add it via `environments.vgo` for the envoy's environment
- Drop it in `usercrates/` if it's per-user

Tracing "why is nginx here" = 5-file scavenger hunt.

### B. Module exclusion — 4 independent paths

- `exclude_modules:` on a match block
- `exclude_modules:` on `environments.vgo`
- Not included upstream (inherited chain stops short)
- `when:` on a role evaluates false → role's modules drop

### C. Per-host variation — 6 independent paths

- Separate match block for that host
- Tag the host + tag-keyed lookup table in a module (`shell: { tag:workstation: /bin/fish, default: /bin/bash }`)
- `environments.vgo` with a single-host "env"
- Template conditional inside the module (`{{ if eq .Traits.hostname "plex" }}...`)
- `when:` on individual resources (`when: "hostname('plex')"`)
- Nest the host under a scoped subdirectory with its own `common.vgo`

### D. Per-environment variation — 4 independent paths

- `environments.vgo` with explicit env blocks
- Nested subdirectory like `stockpile/production/`
- Matchfile `environment:` field with env-keyed `vars:`
- `environment_overrides:` inside a match block

### E. Platform branching — 4 independent paths

- `case:` on a role (`case: { os_family: { linux: [...], darwin: [...] } }`)
- `when: os_family('linux')` on a resource
- Tag/trait-keyed lookup table (`groups: { os_family:linux: [...] }`)
- Conditional var + template substitution

### F. Var resolution — 7-layer priority chain

```
module defaults
  < inherited common.vgo vars
  < envoy-level match-block vars
  < environment_overrides in match block
  < environments.vgo vars
  < conditional vars (when:-guarded values)
  < match vars (when:-guarded with fallback)
  < vars_from: external files
```

Nobody reasons about seven layers reliably. In practice operators pick two and stick with them — but which two isn't standardized.

### G. Matchfile as junk-drawer

A single match block can specify 12 fields. A 40-line match block hides what's load-bearing.

### H. Filename / location conventions are loose

- Matchfiles: conventionally `envoys.vgo`, but ANY `.vgo` file with an `envoys:` block works. Per-host files like `plex.vgo` drop at random depths.
- Roles: `roles.vgo` at root OR legacy `roles/` directory.
- No convention for where to put a customer/site's match blocks — flat, nested, both?

### I. Usercrates vs `modules/users/` — two patterns for the same thing

Old: `modules/users/dan.vgo` listed in `common.vgo`. New: `usercrates/dan.vgo` auto-applies. Nothing stops a mixed stockpile.

### J. Role composition depth

`includes:` is single-level only. Operators try to nest; discover by error message.

### K. Compliance attachment

Two ways to attach a compliance claim to a module: inline `compliance:` block, or directory-level `compliance.vgo`. Both work, but where's the canonical spot?

## Proposed canonical shape

Tier the deprecations by severity of lost flexibility.

### Cheap to lose — delete cleanly

These remove the "every sysadmin invents their own path" surface without hurting real use cases.

1. **`environment:` field + `environment_overrides:` in match blocks.** Redundant with environments.vgo and nested subdirs; used <5 times in the repo today.
2. **`vars_from:` external files.** `common.vgo` at a parent directory covers the DRY use case.
3. **Conditional vars + match vars** (`when:`-guarded var values). Lookup tables inside modules already handle "one var, multiple values per host profile."
4. **`modules/users/` pattern.** Force-migrate to usercrates with a load-time warning for a release, then fatal.
5. **Raw `modules:` lists in match blocks** (soft-warn — allow for genuine leaf exceptions, warn if >3 modules).

### Genuine losses — keep these

These are the paths where deletion would force role-count explosion or 3× LOC for everyday patterns.

6. **`case:` on roles.** Same role, different modules per OS is a real pattern. Deletion forces `web-server-linux` and `web-server-windows` role split plus matchfile logic to select. Keep.
7. **Platform-keyed and tag-keyed lookup tables inside module fields.** `groups: { os_family:linux: [docker], os_family:darwin: [admin] }` is compact and local. Keep.

### Conditional keep

8. **`environments.vgo`.** Delete if your env model is always hierarchical (env = subdirectory). Keep as an escape hatch if env is a cross-cutting axis (e.g., host is prod-in-dc1 AND lab-testing). Document which case applies; if the cross-cutting case never arises for Dan's use, delete.

### Canonical rule per question

After the above, the simplified rulebook:

| Question | One right answer |
|---|---|
| How is a module assigned to an envoy? | A role (defined in `roles.vgo`) that the matchfile assigns, OR inheritance from `common.vgo` at a parent directory. Never inline in the match block. |
| How does a host get per-host config? | Tag it + use tag-keyed lookup tables inside modules. Not separate match blocks unless the host is genuinely bespoke. |
| How does config differ per env? | Nested subdirectories (env = directory) OR tags. Pick one at stockpile bootstrap and stick with it. |
| How does a module branch per OS / platform? | `case:` on the role (for "which modules apply") OR platform-keyed lookup tables (for "which value within a module"). Never inline `when:` on a resource when the axis is platform. |
| How do vars resolve? | 3 layers: module defaults < directory-inherited from `common.vgo` < envoy role/tag combo. Everything else removed. |
| How is a user account configured? | Usercrate in `stockpile/usercrates/` (fleet-wide) or `stockpile/<site>/usercrates/` (scoped). Never `modules/users/`. |
| Where do compliance claims live? | Directory-level `compliance.vgo` in the modules tree, inherited by every module in the subtree. Inline `compliance:` only for module-specific exceptions. |

## New idea: dictionaries for cross-cutting variation

Goal: extract platform-variation VALUES out of modules/matchfiles into a dedicated lookup layer, so modules describe WHAT and dictionaries describe WHICH VALUE per axis.

### Motivation

Today, module fields that need to differ by OS/distribution/release end up with inline lookup tables:

```yaml
# modules/web/nginx.vgo (today)
resources:
  - name: install-nginx
    type: package
    package:
      os_family:linux:   nginx
      os_family:darwin:  nginx
      distribution:alpine: nginx-mod
      default:           nginx
```

This works, but scales badly when 30 modules all have to spell out the same platform map. The variation belongs in one place, not 30.

### Shape

New directory class: `stockpile/dictionaries/<name>.vgo`. Each dictionary file keys a concept on a trait axis and returns a value. Modules reference dict entries with a `dict:` prefix.

```yaml
# stockpile/dictionaries/package-names.vgo
axis: os_family         # which trait this dict keys on
entries:
  nginx:
    linux:    nginx
    darwin:   nginx
    freebsd:  www/nginx
    windows:  nginx
  apache:
    debian:   apache2   # distribution axis overrides os_family inside an entry
    rhel:     httpd
    default:  apache
  postgres:
    debian:   postgresql-14
    rhel:     postgresql14
    darwin:   postgresql@14
    default:  postgresql
```

Module usage becomes value-free:

```yaml
# stockpile/modules/web/nginx.vgo (proposed)
resources:
  - name: install-nginx
    type: package
    package: dict:package-names.nginx
```

The resolver:
1. Reads `package: dict:package-names.nginx`.
2. Looks up `package-names.vgo`, finds the `nginx` entry.
3. Reads the envoy's `os_family` trait (or `distribution`, or `release` — whichever axis the dict declares).
4. Returns the matching value, or the `default` if no match.

### Where dictionaries fit well

**Strong fit** — these axes are universal, narrow, and cross-cut every stockpile:
- OS family (linux/darwin/freebsd/openbsd/netbsd/illumos/windows)
- Distribution (debian/ubuntu/rhel/centos/fedora/alpine/arch/macos-version)
- Release (ubuntu-22.04 vs ubuntu-24.04, rhel-8 vs rhel-9)

Natural dictionaries:
- Package names per OS/distro (`package-names.vgo`)
- Service names per init system (`service-names.vgo`)
- Config file paths per distribution (`config-paths.vgo`)
- Package managers per distro (`package-managers.vgo`)

**Weak fit** — these axes are stockpile-specific, not universal:
- Environment (prod/staging) — each stockpile defines its own envs; no shareable dict
- Tags (workstation/mediaserver) — customer-specific
- Site/location (dc1/office) — operator-specific

Environment variation stays in directories or `environments.vgo`. Tags stay on the envoy. Site is a directory.

### Tradeoffs

**Pros:**
- Modules stay pure — describe desired state without platform noise
- Platform variation lives in ONE file, shared across all modules
- Vigo can ship starter dictionaries (common package/service names for popular apps) that operators override
- Answers "what's the nginx package on Alpine?" by reading one file, not grep-ing 30 modules
- Aligns with the "modules are platform-agnostic recipes" mental model

**Cons:**
- New abstraction — operators learn one more concept
- Indirection — reading a module doesn't tell you the resolved value without also reading the dict
- Debugging adds a layer — "value came from module + dict + fallback"
- Missing-key policy needed: error, or silent fallback? Default is a common trap.
- Overuse risk: operators dict-ify everything and the abstraction becomes noise
- Circular concern: dicts that need to vary by env/tag themselves — fractal

### Proposed constraint: narrow scope

Dictionaries are for **values that vary by universal platform axes only** — OS family, distribution, release. They are NOT for:
- Environment values (prod URL vs staging URL) — use `common.vgo` vars per subdirectory
- Tag/role values (which groups a user gets) — use inline lookup tables or role `case:`
- Per-host one-off values — use a match block (the bespoke escape hatch)

This keeps dictionaries as a **platform-abstraction layer**, not a general-purpose lookup mechanism.

### What dictionaries replace

Strong cases:
- Platform-keyed lookup tables inside modules (for package names, service names, config paths specifically)

Weak cases:
- `case:` on roles — only if the role differs only in module *values*, not in which *modules* apply. Most `case:` uses are "different modules per platform," which dicts don't help.

### Integration with existing axes

Dictionaries would live in the 3-layer var chain:
```
module defaults (with dict: references)
  < directory-inherited common.vgo vars
  < envoy role/tag-keyed vars
```

Resolution order:
1. Read the module field. If it starts with `dict:`, resolve via the dictionary first.
2. Apply normal var substitution + template rendering.
3. Apply any override from the inheritance chain.

## Open decisions for Dan

1. **Which deprecations from the "cheap to lose" list do you want to commit to?** All of them? Subset? Which one hurts your existing stockpile most?
2. **`environments.vgo` — delete or keep?** Depends on whether your env model is always hierarchical. If yes, delete. If you have cross-cutting env, keep.
3. **Dictionaries — yes/no?** If yes, scope limit: do we restrict to OS/distro/release only, or open to any axis?
4. **`modules/users/` deprecation path:** warn for one release then fatal, or fatal immediately?
5. **Matchfile raw `modules:` — soft-warn (above N modules) or hard-fail?**
6. **Role `case:` — keep as-is, or restrict to platform axes only (not arbitrary tag-based branching)?**

## Deliverable sequencing (once decisions land)

1. Update `docs/concepts/multi-axis-config.md` with the simplified canonical model + one resolution-order diagram.
2. Implement deprecation warnings in `server/config/loader.go` for each dropped path.
3. If dictionaries adopted: `stockpile/dictionaries/*.vgo` directory class + loader + `dict:` resolver + docs.
4. Each hard-removal ships as a separate point release with a migration note in the commit message.
5. `vigocli config trace` output extends to show which axis each value came from (module / dict / inherited / match-block / per-env) so operators can debug the reduced chain.
