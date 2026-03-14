# Config Format

All Hobnob configuration lives in YAML files with the `.hnb` extension under `thrall-configs/`. The server watches this directory for changes and hot-reloads automatically.

## Directory Structure

```
thrall-configs/
├── modules/          # Module definitions
│   ├── nginx.hnb
│   ├── postgres.hnb
│   └── monitoring.hnb
├── roles/            # Role definitions
│   ├── webserver.hnb
│   └── database.hnb
└── nodes.hnb         # Node-to-role/module mappings
```

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
    targetpath: /etc/nginx/nginx.conf
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
| `environment` | no | Environment name for env_vars resolution |
| `roles` | no | List of role names to assign |
| `modules` | no | Additional modules (beyond those from roles) |
| `vars` | no | Variable overrides (override module defaults) |
| `env_vars` | no | Per-environment variable overrides |
| `vars_from` | no | External var files to load (relative to thrall-configs/) |

## Variable Resolution

Variables are resolved in this order (last wins):

```
Module vars (defaults)
    ↓ overridden by
Node-level vars
    ↓ overridden by
env_vars[environment]
    ↓ overridden by
Conditional vars (trait-based)
```

### env_vars

Override variables per environment:

```yaml
thralls:
  - match: "web*.example.com"
    environment: production
    roles: [webserver]
    vars:
      nginx_port: 80
      log_level: info
    env_vars:
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

### Conditional Vars

Variables that evaluate based on thrall traits:

```yaml
vars:
  is_debian:
    trait: os.family
    in: [debian, ubuntu]
  has_enough_ram:
    trait: memory.total_mb
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
| `subscribes_secret` | no | Secret paths to watch — re-apply when rotated |

Plus type-specific attributes (see [Executors](../executors/_index.md)).

## Hot Reload

The server watches `thrall-configs/` with fsnotify. When any `.hnb` file changes:
1. The entire config tree is re-parsed
2. Module definitions, role definitions, and node mappings are rebuilt
3. The next agent check-in gets the updated config

No server restart needed. Config parse errors are logged and trigger an SMTP notification if configured.

## Related

- [Resource DAG](resource-dag.md) — Dependency ordering
- [Resource Language](resource-language.md) — Advanced config features
- [When Expressions](when-expressions.md) — Conditional logic
- [Templates](templates.md) — Go templates in content: attributes
