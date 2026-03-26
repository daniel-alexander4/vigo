
# Config Format

All Vigo configuration lives in YAML files with the `.vgo` extension under `stack/stage/`. The server reloads config when `vigogo config publish` syncs files to `.live/`.

## Directory Structure

The config directory doubles as a envoy hierarchy. Subdirectories define inheritance — `common.vgo` at any level defines modules, roles, and vars inherited by all entries in subdirectories below it.

```
stack/stage/
├── modules/              # Module definitions (unchanged)
│   ├── nginx.vgo
│   ├── postgres.vgo
│   └── monitoring.vgo
├── templates/            # Template files for source: references
├── roles.vgo             # All role definitions in one file
├── common.vgo            # Modules/roles/vars inherited by ALL subdirs
├── waivers.vgo           # Compliance waivers (optional, inherits like common.vgo)
├── production/
│   ├── common.vgo        # Inherited by all production subdirs
│   └── web/
│       └── web.vgo       # Leaf entry: match: "web*.prod.example.com"
└── staging/
    └── staging.vgo       # Leaf entry: match: "*.staging.example.com"
```

**Key conventions:**
- `common.vgo` at any directory level = inheritance definition (no `match:`). Applies only to subdirectories.
- `waivers.vgo` at any directory level = compliance waivers (optional). Same inheritance pattern.
- Any other `.vgo` file = leaf entry with `match:` patterns mapping hostnames to modules.
- `roles.vgo` at root = single file containing all role definitions.
- `modules/` and `templates/` = unchanged (modules are a flat library, templates are referenced by explicit `source:` path).

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
| `vars` | no | Default variable values (overridden by node-level vars) |
| `defaults` | no | Default attributes applied to all resources in this module |
| `depends_on` | no | Other modules this module depends on (module-level ordering) |
| `before` | no | Modules that must run after this module |
| `resources` | yes | List of resources to manage |

## Roles

A role is a named list of modules. Roles provide a layer of abstraction — assign a role to a envoy instead of listing individual modules.

```yaml
name: webserver
modules:
  - nginx
  - logrotate
  - name: monitoring
    when: "!is_container"
```

Modules in a role can be plain strings or objects with a `when:` expression for conditional inclusion.

### Conditional Role References

Role references in node mappings support the same scalar-or-object syntax. Use this to apply different roles based on OS or other traits:

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

When a role ref has a `when:` condition, all modules from that role inherit the condition (unless the module already has its own `when:`, which takes precedence). This lets you write one node mapping that works across your entire fleet.

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

## Node Mappings (nodes.vgo)

The `nodes.vgo` file maps envoy hostnames to roles and modules. **First match wins** — the server tries each entry in order and uses the first one whose `match` glob matches the envoy's hostname.

```yaml
envoys:
  - match: "web*.prod.example.com"
    environment: production
    roles: [webserver]
    vars:
      nginx_port: 443

  - match: "web*.staging.example.com"
    environment: staging
    roles: [webserver]
    vars:
      nginx_port: 8080

  - match: "db*.example.com"
    roles: [database]
    modules: [monitoring]

  - match: "*"
    modules: [base, monitoring]
```

### Node Entry Fields

| Field | Required | Description |
|-------|----------|-------------|
| `match` | yes | Hostname glob pattern (e.g., `web*.example.com`, `*`) |
| `environment` | no | Environment name for environment_overrides resolution |
| `roles` | no | List of role names to assign |
| `modules` | no | Additional modules (beyond those from roles) |
| `vars` | no | Variable overrides (override module defaults) |
| `environment_overrides` | no | Per-environment variable overrides |
| `vars_from` | no | External var files to load (relative to stack/stage/) |
| `exclude_modules` | no | Opt out of specific inherited modules (e.g., `[monitoring]`) |

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
- Use `vigogo config trace <hostname>` to see the full inheritance chain for a specific host.
- Use `vigogo config tree` to see the entire directory hierarchy at a glance.
- Use `vigogo config search --module <name>` to find which entries use a specific module.

## Compliance Waivers

Place a `waivers.vgo` file at any directory level to waive specific compliance controls. Waivers follow the same inheritance pattern as `common.vgo` — root-level waivers apply fleet-wide, subdirectory waivers apply only to that subtree.

Waived controls count as "accepted" in compliance scoring and are displayed distinctly on dashboards (not hidden).

```yaml
# waivers.vgo
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

Fields:
- `control` — control ID matching the compliance tag in the module
- `reason` — justification (required)
- `approved_by` — who approved (required)
- `expires` — optional expiration date (YYYY-MM-DD). Expired waivers are automatically excluded.

## Variable Resolution

Variables are resolved in this order (last wins):

```
Module vars (defaults)
    ↓ overridden by
Node-level vars
    ↓ overridden by
environment_overrides[environment]
    ↓ overridden by
Conditional vars (trait-based)
```

### environment_overrides

Override variables per environment:

```yaml
envoys:
  - match: "web*.example.com"
    environment: production
    roles: [webserver]
    vars:
      nginx_port: 80
      log_level: info
    environment_overrides:
      staging:
        nginx_port: 8080
        log_level: debug
      production:
        nginx_port: 443
```

### vars_from

Load variables from external files:

```yaml
envoys:
  - match: "db*.example.com"
    roles: [database]
    vars_from: ["vars/database-credentials.vgo"]
```

The referenced file contains a flat YAML map:

```yaml
# vars/database-credentials.vgo
db_user: postgres
db_password: "secret:vigo/db/password"
db_port: 5432
```

#### Hostname Substitution in vars_from

Use `{{ .Hostname }}` in `vars_from` paths to load host-specific var files:

```yaml
envoys:
  - match: "web*.example.com"
    roles: [webserver]
    vars_from:
      - "vars/web-common.vgo"
      - "vars/{{ .Hostname }}.vgo"
```

This lets a single node entry serve many envoys while each loads its own overrides. Missing var files are silently skipped — the envoy still gets all vars from other sources.

### Conditional Vars

Variables that evaluate based on envoy traits:

```yaml
vars:
  is_debian:
    trait: os.family
    in: [debian, ubuntu]
  has_enough_ram:
    trait: hardware.memory_mb
    gte: 4096
```

Operators: `in`, `not_in`, `gt`, `lt`, `gte`, `lte`, `regex`.

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

Plus type-specific attributes (see [Executors](../reference/executors/_index.md)).

## Auto-Comments (`#~`)

When you run `vigogo config publish`, the module linter automatically adds `#~` prefixed comments to changed modules:

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
- The linter also repairs YAML issues (tabs, unquoted booleans) and normalizes key ordering. See [Config Publish Pipeline](../operations/config-publish.md#module-linter) for details.

## Config Reload

After `vigogo config publish` syncs files to `.live/`, it calls the server's reload endpoint. The server:
1. Re-parses the entire config tree
2. Rebuilds module definitions, role definitions, and node mappings
3. The next agent check-in gets the updated config

No server restart needed. Config parse errors are logged and trigger an SMTP notification if configured.

## Related

- [Resource DAG](resource-dag.md) — Dependency ordering
- [Resource Language](resource-language.md) — Advanced config features
- [When Expressions](when-expressions.md) — Conditional logic
- [Templates](templates.md) — Go templates in content: attributes
