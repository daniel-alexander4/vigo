
# Config Format

All Vigo configuration lives in YAML files with the `.vgo` extension under `stockpile/`. The server reloads config when `vigocli config publish` syncs files to `.live/`.

## Directory Structure

The config directory doubles as a envoy hierarchy. Subdirectories define inheritance — `common.vgo` at any level defines modules, roles, and vars inherited by all entries in subdirectories below it.

```
stockpile/
├── modules/                     # Module definitions (unchanged)
│   ├── nginx.vgo
│   ├── postgres.vgo
│   ├── compliance.vgo           # Module-tree: fleet-wide claims inherited by every module
│   ├── monitoring.vgo
│   └── users/
│       ├── compliance.vgo       # Module-tree: claims inherited by modules/users/*.vgo
│       └── dan.vgo
├── templates/                   # Template files for source: references
├── roles.vgo                    # All role definitions in one file
├── common.vgo                   # Modules/roles/vars inherited by ALL subdirs
├── compliance.vgo               # Envoys-tree: compliance waivers (optional, inherits like common.vgo)
├── production/
│   ├── common.vgo               # Inherited by all production subdirs
│   └── web/
│       └── web.vgo              # Leaf entry: match: "web*.prod.example.com"
└── staging/
    └── staging.vgo              # Leaf entry: match: "*.staging.example.com"
```

**Key conventions:**
- `common.vgo` at any directory level = inheritance definition (no `match:`). Applies only to subdirectories.
- `compliance.vgo` at any directory level = compliance configuration. **Location determines semantics:** files in the envoys tree (outside `modules/`) contribute waivers; files in the modules tree contribute per-module compliance claims inherited by every module in the same subtree. See [Compliance Waivers + Claims](#compliance-waivers--claims) below and the [Lookup Tables concept](lookups.md) for per-envoy resource variation.
- Any other `.vgo` file outside `modules/` = leaf entry with `match:` patterns mapping hostnames to modules.
- `roles.vgo` at root = single file containing all role definitions.
- `modules/` holds a flat library of module definitions. **One exception:** `compliance.vgo` files under `modules/` are not module definitions — they are directory-level compliance claim declarations (see below). The loader recognizes the filename and routes it through a separate walker; you can drop a `compliance.vgo` at any depth under `modules/` without the file being mistaken for a module. Every other `.vgo` file under `modules/` is treated as a module definition and must have `name:` and `resources:` fields.
- `templates/` = unchanged (templates are referenced by explicit `source:` path from within modules).

### Why `compliance.vgo` under `modules/` is a special case

Vigo's module loader walks every `.vgo` file under `modules/` and parses it as a module definition. A module needs a `name:` field and (usually) a `resources:` block. A directory-level compliance claim declaration has neither — it only holds `compliance:` and optionally `waivers:` — so if the loader tried to parse it as a module it would reject the file with "missing required name field" or "module has no resources".

Rather than force operators to put compliance claim files in a sibling directory (which would break the "claims live next to the modules they apply to" ergonomics we wanted), the loader knows that any file literally named `compliance.vgo` under `modules/` is skipped by the module walker and picked up by a separate directory-level walker that unions its claims into every module in the same subtree. Publish-time lint knows about the same exception — it won't run modlint against `compliance.vgo` files — and so does the secret-scanner.

This is the only file-name special case in the module library today. If you ever introduce a `compliance.vgo` under `modules/` and see a lint error about missing `name:` or empty `resources:`, it means you're running against a vigo build older than 0.21.3 and need to rebuild. No other file name under `modules/` gets this treatment — everything else is parsed as a module.

The legacy layout with `envoys/`, `roles/`, and `vars/` directories is still supported for backward compatibility.

## Modules

A module is a reusable set of resources. It defines packages to install, files to manage, services to run, etc.

```yaml
name: nginx

vars:
  nginx_port: 80
  worker_connections: 1024

resources:
  - name: nginx-package
    type: package
    package: nginx

  - name: nginx-config
    type: file
    target_path: /etc/nginx/nginx.conf
    content: |
      worker_connections {{ .Vars.worker_connections }};
    depends_on: [nginx-package]
    notify: [restart-nginx]
  - name: nginx-service
    type: service
    service: nginx
    state: running
    enabled: true
    depends_on: [nginx-package]

  - name: restart-nginx
    type: service
    service: nginx
    state: restarted
    when: "changed"
```

### Module Fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Unique module name |
| `vars` | no | Default variable values (overridden by envoy-level vars) |
| `defaults` | no | Default attributes applied to all resources in this module |
| `depends_on` | no | Other modules this module depends on (module-level ordering) |
| `before` | no | Modules that must run after this module |
| `resources` | yes | List of resources to manage |

## Roles

A role is a named list of modules. Roles provide a layer of abstraction — assign a role to an envoy instead of listing individual modules.

```yaml
name: webserver
modules:
  - nginx
  - logrotate
  - name: monitoring
    when: "!is_container"
```

Modules in a role can be plain strings or objects with a `when:` expression for conditional inclusion.

### Role-Level When

A role definition can have its own `when:` expression. All modules in the role inherit the condition unless they have their own:

```yaml
- name: cis-ubuntu
  when: "distro('ubuntu')"
  modules: [cis-ubuntu-access, cis-ubuntu-network, cis-ubuntu-logging]
```

### Conditional Case

For roles that need different modules per OS or platform, use `case:`. Each branch has a `when:` and its own module list. All matching cases contribute modules:

```yaml
- name: remote-access
  case:
    - when: "os_family('linux')"
      modules: [x11vnc, xrdp]
    - when: "os_family('windows')"
      modules: [tightvnc]
```

A role can have unconditional `modules:` and `case:` together — the unconditional modules always apply, cases apply conditionally:

```yaml
- name: monitoring
  modules: [node-exporter]        # always applied
  case:
    - when: "os_family('linux')"
      modules: [collectd]
    - when: "os_family('windows')"
      modules: [windows-exporter]
```

### Conditional Role References

Role references in match blocks support the same scalar-or-object syntax. Use this to apply different roles based on OS or other traits:

```yaml
envoys:
  - match: "*.example.com"
    roles:
      - name: cis-ubuntu
        when: "distro('ubuntu')"
      - name: cis-rhel
        when: "distro('rhel') || distro('centos') || distro('rocky')"
      - name: cis-windows
        when: "os_family('windows')"
```

When a role ref has a `when:` condition, all modules from that role inherit the condition (unless the module already has its own `when:`, which takes precedence). This lets you write one match block that works across your entire fleet.

### When Inheritance

When expressions are inherited with most-specific-wins precedence:

1. Module's own `when:` (highest priority)
2. Case `when:`
3. Role definition `when:`
4. Role ref `when:` (at the match block level)

### Role Includes

Roles can include other roles using `includes:` to compose shared module sets:

```yaml
name: webserver
includes:
  - base-security
modules:
  - nginx
  - logrotate
```

Included role modules are prepended before the role's own modules. Includes are **single level only** — an included role cannot itself have `includes:`. Duplicate modules across includes and the role's own list are automatically deduplicated (first occurrence wins).

## Matchfile (envoys.vgo)

The `envoys.vgo` file maps envoy hostnames to roles and modules. **First match wins** — the server tries each entry in order and uses the first one whose `match` glob matches the envoy's hostname.

```yaml
envoys:
  - match: "web*.prod.example.com"
    environment: production
    roles: [webserver]
    tags: [webserver, production]
    vars:
      nginx_port: 443

  - match: "web*.staging.example.com"
    environment: staging
    roles: [webserver]
    tags: [webserver, staging]
    vars:
      nginx_port: 8080

  - match: "db*.example.com"
    roles: [database]
    tags: [database]
    modules: [monitoring]

  - match: "*"
    modules: [base, monitoring]
```

### Node Entry Fields

| Field | Required | Description |
|-------|----------|-------------|
| `match` | yes | Hostname glob pattern (e.g., `web*.example.com`, `*`) |
| `environment` | no | Environment name used to select the matching block in `per_env_overrides.vgo` |
| `roles` | no | List of role names to assign |
| `modules` | no | Additional modules (beyond those from roles) |
| `vars` | no | Variable overrides (override module defaults and inherited `common.vgo` vars) |
| `exclude_modules` | no | Opt out of specific inherited modules (e.g., `[monitoring]`) |
| `exclude_usercrates` | no | Opt out of auto-applied usercrates (e.g., `[alice]`) |
| `tags` | no | Static tags for targeting — referenced by `vigocli` as `tag:<name>` and by [lookup tables](lookups.md) in resource attributes |

### Tags

Tags are labels attached to envoys at config-resolution time. They are the single source of truth for envoy classification — **the matchfile is the only writer**. There is no admin API to set tags out of band; editing this file and publishing is the only way to tag an envoy.

Tags are synced into the in-memory fleet index on every check-in. Once synced, they are available to:

- Admin targeting (`vigocli task ... --target tag:webserver`, `vigocli query ... --target tag:database`)
- Per-envoy field variation in resource attributes via [lookup tables](lookups.md)

Because the matchfile is the only writer, tagging is deterministic and reproducible. A fresh server restart rebuilds tags by matching envoy hostnames against the current matchfile — no DB state to drift from config.

## Directory Inheritance

Instead of listing every module on every envoy entry, define shared modules in `common.vgo` files at directory levels. Everything in subdirectories inherits automatically.

```yaml
# common.vgo (at root) — all subdirs get these
modules: [sshd, ntp, monitoring]
vars:
  dns_server: "1.1.1.1"
```

```yaml
# production/common.vgo — production subdirs also get these
modules: [log-shipping, auditd]
vars:
  log_level: warn
```

```yaml
# production/web/web.vgo — leaf entry
envoys:
  - match: "web*.prod.example.com"
    modules: [nginx, certbot]
    vars:
      nginx_port: 443
```

Result for `web01.prod.example.com`: modules `sshd, ntp, monitoring, log-shipping, auditd, nginx, certbot` with vars `dns_server=1.1.1.1, log_level=warn, nginx_port=443`.

**Rules:**
- Parent modules come **before** child modules in the DAG (foundation first).
- Child vars **override** parent vars of the same name.
- `exclude_modules:` on a leaf entry removes specific inherited modules:
  ```yaml
  envoys:
    - match: "docker*.example.com"
      modules: [docker]
      exclude_modules: [ntp]  # containers use host clock
  ```
- Root-level `common.vgo` applies only to subdirectories — leaf entries at the root level are self-contained.
- Use `vigocli config trace <hostname>` to see the full inheritance chain for a specific host.
- Use `vigocli config tree` to see the entire directory hierarchy at a glance.
- Use `vigocli config search --module <name>` to find which entries use a specific module.

## Usercrates

Usercrates are per-user modules (user account + home-dir config + dotfiles, typically) that live in a dedicated `stockpile/**/usercrates/` directory class and **auto-apply to every envoy within that directory's scope** — no need to list them in `common.vgo`. See the [Glossary](../../reference/glossary.md#usercrate) for what the term means and why it's distinct from a regular module.

```
stockpile/
  usercrates/
    admin.vgo              # fleet-wide — applies to every envoy
  customerA/
    envoys.vgo             # matchfile for customerA envoys
    usercrates/
      alice.vgo            # only applies under customerA/
  customerB/
    envoys.vgo
    usercrates/
      bob.vgo              # only applies under customerB/
```

Scope rule: a usercrate at `stockpile/<path>/usercrates/<name>.vgo` applies to every envoy whose matchfile lives at `stockpile/<path>/` or deeper. Mirrors the `common.vgo` inheritance chain, with one key difference: root-level usercrates at `stockpile/usercrates/` apply to root-level envoys too (unlike root `common.vgo`, which only applies to subdirectories).

### Auto-apply vs. explicit listing

Usercrates are the **only** auto-apply class. Regular modules still require explicit listing via `modules:` in `common.vgo` or a match block. The asymmetry is intentional: modules serve two purposes (direct application AND building blocks for roles), so they need explicit selection. Usercrates are always direct application — there's no "usercrate consumed by a role" pattern — so the filesystem path IS the scope.

### Opt-out

Drop a named usercrate from a specific envoy:

```yaml
# envoys.vgo
envoys:
  - match: "kiosk-*.example.com"
    roles: [kiosk]
    exclude_usercrates:
      - users/dan     # kiosks don't need dev accounts
      - users/alice
```

Drop a usercrate fleet-wide (or for everything under a subdirectory) by setting `exclude_usercrates:` on a `common.vgo`:

```yaml
# stockpile/production/common.vgo
exclude_usercrates:
  - users/dev-only-account   # prod excludes this user everywhere
```

`per_env_overrides.vgo` blocks also support `exclude_usercrates:` with identical semantics.

### Name namespace

Usercrates and regular modules share one namespace — the `name:` field in a usercrate must be globally unique across all modules and usercrates. Load-time collision is a fatal error with both source paths reported. This keeps `modules:` references in roles + common.vgo unambiguous (they resolve to a module OR a usercrate, never both).

### User management specifically

See [User Management](usercrates.md) for why user accounts benefit from the usercrate pattern specifically — per-person audit trail, lookup-table-driven per-envoy variation, two-phase retirement.

## Compliance Waivers + Claims

A `compliance.vgo` file holds both compliance waivers and per-module compliance claims. It is the single file format used for directory-level compliance configuration, and its location tells vigo what it affects:

- **Envoys-tree** (`stockpile/compliance.vgo`, `stockpile/production/compliance.vgo`, etc. — any location outside `modules/`): the `waivers:` block is active. The `compliance:` block, if present, is ignored with a warning (there are no modules at this location for claims to attach to).
- **Modules-tree** (`stockpile/modules/compliance.vgo`, `stockpile/modules/users/compliance.vgo`, etc.): the `compliance:` block is active — claims are unioned into every module in the same subtree. The `waivers:` block, if present, is ignored with a warning (there are no envoys at this location for waivers to apply to).

Both keys are always parseable in the same file format; you just get to choose where to put the file based on which dimension you're scoping.

**Waivers** follow the same inheritance pattern as `common.vgo` — root-level waivers apply fleet-wide, subdirectory waivers apply only to envoys whose config is derived from that subtree. Waived controls count as "accepted" in compliance scoring and are displayed distinctly on dashboards (not hidden).

**Claims** from a modules-tree `compliance.vgo` are unioned into every module defined anywhere beneath that file. A module at `modules/users/dan.vgo` inherits claims from `modules/users/compliance.vgo` AND from `modules/compliance.vgo` if both exist, plus whatever the module itself declares in its own `compliance:` block. All claims across all sources are unioned with dedupe.

```yaml
# stockpile/compliance.vgo — envoys tree, waivers only
waivers:
  cis-ubuntu:
    - control: 6.1.10
      reason: "Build artifacts require world-writable tmp"
      approved_by: dan
      expires: 2027-01-01

    - control: 6.2.5
      reason: "Home directories managed by LDAP"
      approved_by: dan
```

```yaml
# stockpile/modules/users/compliance.vgo — modules tree, claims only
compliance:
  cis: [5.4.1]
  nist-800-53: [AC-3, AC-6, AC-6(1), IA-2, IA-5]
  pci-dss: [8.1.1, 8.1.2, 8.1.4]
  soc2: [CC6.1, CC6.2, CC6.3]
  hipaa: [164.312(a)(1), 164.312(a)(2)(i), 164.312(d)]
```

**Waiver fields:**
- `control` — control ID matching the compliance tag in the module
- `reason` — justification (required)
- `approved_by` — who approved (required)
- `expires` — optional expiration date (YYYY-MM-DD). Expired waivers are automatically excluded.

### How the modules-tree walker avoids collisions with regular modules

This is the part that bit us on the first publish after shipping the feature, so it's worth spelling out: `compliance.vgo` under `modules/` is **not a module file**, and the loader explicitly refuses to parse it as one. Vigo's module library is otherwise a flat rule — "every `.vgo` file under `modules/` is a module definition with `name:` and `resources:`" — so adding directory-level compliance files required introducing a single named exception.

The exception lives in three places in the code, all keyed off the literal filename `compliance.vgo`:

1. **Config loader** (`server/config/loader.go:loadModuleDefs`): iterates `.vgo` files under `modules/` and skips any file whose basename is `compliance.vgo`. Those files are not parsed as modules. A separate walker (`loadModuleComplianceConfigs`) picks them up and builds a directory-keyed claim map, which is then unioned into each `ModuleDefinition.Compliance` at module-load time via `applyModuleComplianceInheritance`.
2. **Publish-time modlint** (`server/publish/publish.go:lintModules`): the pipeline that runs `modlint` on every changed file under `modules/` on its way to `.live/`. Modlint validates module shape (`name:`, `resources:`, idempotency rules, etc.) and would reject `compliance.vgo` outright. This walker has the same basename skip as the loader so the publish pipeline doesn't trip on the exception.
3. **Directory-level walker** (`server/config/loader.go:loadModuleComplianceConfigs`): positively selects `compliance.vgo` files under `modules/` and reads only the `compliance:` key. Any `waivers:` block found here is ignored with a warning — the modules tree has no envoy dimension for a waiver to apply to, so the key is dead in this location.

The only valid top-level keys in a `compliance.vgo` file are `compliance:` and `waivers:`. The parser rejects any other top-level key with a clear error. Modules that need their own inline `compliance:` block still can — those claims union with whatever ancestor directory files declare.

**Why not a different filename?** We considered making the modules-tree file a different name (e.g., `claims.vgo`) to avoid the shared-namespace exception altogether. The reason we didn't: the envoys-tree and modules-tree files are structurally identical — both are YAML with `compliance:` and/or `waivers:` — and having two different names for the same file format would have been worse than one name with a location-keyed meaning. Operators write the file once, put it where it fits their intent, and `modlint` tells them if the wrong key is in the wrong tree.

**Migration note:** Older vigo versions used `waivers.vgo` as the filename. The filename is now `compliance.vgo`. When vigo encounters a legacy `waivers.vgo` file, it logs a loud error ("`waivers.vgo` has been renamed to `compliance.vgo` — rename this file and republish to restore waiver enforcement") and skips the file. The rest of the config still loads; waiver enforcement from the skipped file is suspended until you rename it. This downgrade to non-fatal was added in 0.21.4 — in 0.21.2 through 0.21.3 the rejection was fatal and could leave the server in an unrecoverable zombie state on upgrade. If you have multiple `waivers.vgo` files at different directory levels, rename each of them; content is compatible, only the filename changes.

## Variable Resolution

Variables are resolved in three layers (last wins):

```
Module vars (defaults)
    ↓ overridden by
Inherited common.vgo vars (parent → child)
    ↓ overridden by
Match-block vars
    ↓ overridden by
per_env_overrides.vgo vars (for the match block's environment:)
```

Tag-keyed and platform-keyed lookup tables inside module fields then resolve final field values based on the envoy's tags and traits. See [Lookup Tables](lookups.md) for the per-trait/per-tag variation mechanism.

For environment-specific values, use `per_env_overrides.vgo` at the appropriate directory level — not per-match-block overrides. See [Multi-Axis Config](multi-axis-config.md) for the canonical model and why earlier mechanisms (`environment_overrides:`, `vars_from:`, conditional vars) were removed in 0.27.0.

## Resource Format

Every resource has these common fields:

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Unique name within the module |
| `type` | yes | Executor type (file, package, service, etc.) |
| `state` | no | Target state, usually `present` (default) or `absent` |
| `when` | no | Conditional expression — skip if false |
| `depends_on` | no | Resources that must succeed before this one |
| `before` | no | Resources that must run after this one |
| `notify` | no | Resources (or modules) to trigger when this one changes |
| `subscribes` | no | Resources (or modules) to watch — re-apply self when they change |
| `watch_secret` | no | Secret paths to watch — re-apply when rotated |

Plus type-specific attributes (see [Executors](../../reference/executors/_index.md)).

## Auto-Comments (`#~`)

When you run `vigocli config publish`, the module linter automatically adds `#~` prefixed comments to changed modules:

```yaml
#~ Manages package, file, service
name: nginx

resources:
  #~ Install nginx package
  - name: nginx-package
    type: package
    package: nginx

  #~ Deploy /etc/nginx/sites-available/default
  - name: nginx-config
    type: file
    target_path: /etc/nginx/sites-available/default
    content: |
      server {
          listen {{ .Vars.nginx_port }};
      }
    owner: root
    group: root
    mode: "0644"
    notify: [nginx-service]

  # Custom note: we use reloaded here because nginx supports graceful reload
  #~ Reload nginx on dependency change
  - name: nginx-service
    type: service
    service: nginx
    state: reloaded
    when: changed
    subscribes: [nginx-config]
```

**Rules:**
- `#~` comments are auto-generated — they are stripped and regenerated on every publish. Do not edit them.
- `#` comments (no tilde) are hand-written and never touched by the linter. Use these for your own notes.
- The linter also repairs YAML issues (tabs, unquoted booleans) and normalizes key ordering. See [Config Publish Pipeline](../../operations/config-publish.md#module-linter) for details.

## Config Reload

After `vigocli config publish` syncs files to `.live/`, it calls the server's reload endpoint. The server:
1. Re-parses the entire config tree
2. Rebuilds module definitions, role definitions, and match blocks
3. The next agent check-in gets the updated config

No server restart needed. Config parse errors are logged and trigger an SMTP notification if configured.

## Related

- [Resource DAG](../architecture/resource-dag.md) — Dependency ordering
- [Resource Language](resource-types.md) — Advanced config features
- [When Expressions](when-expressions.md) — Conditional logic
- [Templates](templates.md) — Go templates in content: attributes
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
