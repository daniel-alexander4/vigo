# Config Format

All Hobnob configuration lives in YAML files with the `.hnb` extension under `hnb/proposed/`. The server reloads config when `hobnobcli config publish` syncs files to `.live/`.

## Directory Structure

The config directory doubles as a thrall hierarchy. Subdirectories define inheritance — `common.hnb` at any level defines modules, roles, and vars inherited by all entries in subdirectories below it.

```
hnb/proposed/
├── modules/              # Module definitions (unchanged)
│   ├── nginx.hnb
│   ├── postgres.hnb
│   └── monitoring.hnb
├── templates/            # Template files for source: references
├── roles.hnb             # All role definitions in one file
├── common.hnb            # Modules/roles/vars inherited by ALL subdirs
├── production/
│   ├── common.hnb        # Inherited by all production subdirs
│   └── web/
│       └── web.hnb       # Leaf entry: match: "web*.prod.example.com"
└── staging/
    └── staging.hnb       # Leaf entry: match: "*.staging.example.com"
```

**Key conventions:**
- `common.hnb` at any directory level = inheritance definition (no `match:`). Applies only to subdirectories.
- Any other `.hnb` file = leaf entry with `match:` patterns mapping hostnames to modules.
- `roles.hnb` at root = single file containing all role definitions.
- `modules/` and `templates/` = unchanged (modules are a flat library, templates are referenced by explicit `source:` path).

The legacy layout with `thralls/`, `roles/`, and `vars/` directories is still supported for backward compatibility.

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
    notify: [nginx-service]

  - name: nginx-service
    type: service
    service: nginx
    state: running
    enabled: true
    depends_on: [nginx-package]
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

A role is a named list of modules. Roles provide a layer of abstraction — assign a role to a thrall instead of listing individual modules.

```yaml
name: webserver
modules:
  - nginx
  - logrotate
  - name: monitoring
    when: "!is_container"
```

Modules in a role can be plain strings or objects with a `when:` expression for conditional inclusion.

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

## Node Mappings (nodes.hnb)

The `nodes.hnb` file maps thrall hostnames to roles and modules. **First match wins** — the server tries each entry in order and uses the first one whose `match` glob matches the thrall's hostname.

```yaml
thralls:
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
| `vars_from` | no | External var files to load (relative to hnb/proposed/) |
| `exclude_modules` | no | Opt out of specific inherited modules (e.g., `[monitoring]`) |

## Directory Inheritance

Instead of listing every module on every thrall entry, define shared modules in `common.hnb` files at directory levels. Everything in subdirectories inherits automatically.

```yaml
# common.hnb (at root) — all subdirs get these
modules: [sshd, ntp, monitoring]
vars:
  dns_server: "1.1.1.1"
```

```yaml
# production/common.hnb — production subdirs also get these
modules: [log-shipping, auditd]
vars:
  log_level: warn
```

```yaml
# production/web/web.hnb — leaf entry
thralls:
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
  thralls:
    - match: "docker*.example.com"
      modules: [docker]
      exclude_modules: [ntp]  # containers use host clock
  ```
- Root-level `common.hnb` applies only to subdirectories — leaf entries at the root level are self-contained.
- Use `hobnobcli config trace <hostname>` to see the full inheritance chain for a specific host.
- Use `hobnobcli config tree` to see the entire directory hierarchy at a glance.
- Use `hobnobcli config search --module <name>` to find which entries use a specific module.

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
thralls:
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
thralls:
  - match: "db*.example.com"
    roles: [database]
    vars_from: ["vars/database-credentials.hnb"]
```

The referenced file contains a flat YAML map:

```yaml
# vars/database-credentials.hnb
db_user: postgres
db_password: "secret:hobnob/db/password"
db_port: 5432
```

#### Hostname Substitution in vars_from

Use `{{ .Hostname }}` in `vars_from` paths to load host-specific var files:

```yaml
thralls:
  - match: "web*.example.com"
    roles: [webserver]
    vars_from:
      - "vars/web-common.hnb"
      - "vars/{{ .Hostname }}.hnb"
```

This lets a single node entry serve many thralls while each loads its own overrides. Missing var files are silently skipped — the thrall still gets all vars from other sources.

### Conditional Vars

Variables that evaluate based on thrall traits:

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
| `notify` | no | Resources to re-apply when this one changes |
| `subscribes` | no | Resources to watch — re-apply self when they change |
| `watch_secret` | no | Secret paths to watch — re-apply when rotated |

Plus type-specific attributes (see [Executors](../executors/_index.md)).

## Auto-Comments (`#~`)

When you run `hobnobcli config publish`, the module linter automatically adds `#~` prefixed comments to changed modules:

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
    subscribes: [nginx-config]
```

**Rules:**
- `#~` comments are auto-generated — they are stripped and regenerated on every publish. Do not edit them.
- `#` comments (no tilde) are hand-written and never touched by the linter. Use these for your own notes.
- The linter also repairs YAML issues (tabs, unquoted booleans) and normalizes key ordering. See [Config Publish Pipeline](../operations/config-publish.md#module-linter) for details.

## Config Reload

After `hobnobcli config publish` syncs files to `.live/`, it calls the server's reload endpoint. The server:
1. Re-parses the entire config tree
2. Rebuilds module definitions, role definitions, and node mappings
3. The next agent check-in gets the updated config

No server restart needed. Config parse errors are logged and trigger an SMTP notification if configured.

## Related

- [Resource DAG](resource-dag.md) — Dependency ordering
- [Resource Language](resource-language.md) — Advanced config features
- [When Expressions](when-expressions.md) — Conditional logic
- [Templates](templates.md) — Go templates in content: attributes
