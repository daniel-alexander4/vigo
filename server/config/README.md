# server/config -- Configuration Loader

The config system loads module, role, and node definitions from YAML files (`.hnb`), resolves secrets, renders Go templates, and watches for changes via fsnotify.

## Summary

- Three config layers: **modules** -> **roles** -> **nodes**
- YAML syntax with `.hnb` file extension (`.yaml` and `.yml` also accepted)
- Secret references resolved at load time (`secret:` prefix)
- Go template syntax with `.Traits` and `.Vars` namespaces
- Hot-reload: fsnotify watches config directories, bumps policy version on change
- First-match-wins for node mapping
- Recursive directory loading via `filepath.WalkDir`
- Idempotency validation at load time (bypass with `--dangerous`)

## Config Hierarchy

```
node-configs/
├── modules/           # Reusable resource sets with default vars
│   ├── nginx.hnb
│   ├── openssh.hnb
│   └── monitoring/    # Subdirectories supported
│       └── prometheus.hnb
├── roles/             # Named lists of modules
│   └── webserver.hnb
├── nodes/             # Map hostnames to roles + modules
│   ├── production.hnb
│   └── staging.hnb
└── templates/         # External template files referenced by source:
    └── nginx/
        └── nginx.conf.tmpl
```

All directories are scanned recursively. Files ending in `.example` are ignored.

## Module Format

A module defines a reusable set of resources with default variables.

```yaml
name: nginx

vars:
  port: "80"
  worker_count: "auto"

resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present

  - name: nginx-config
    type: file
    targetpath: /etc/nginx/nginx.conf
    content: |
      worker_processes {{ .Vars.worker_count }};
      server {
        listen {{ .Vars.port }};
        server_name {{ .Traits.network.hostname }};
      }
    mode: "0644"
    depends_on: [install-nginx]
    notify: [nginx-service]

  - name: nginx-service
    type: service
    service: nginx
    state: running
    enabled: true
```

### External template files

Instead of inline `content`, use `source` to reference a file relative to the node-configs directory:

```yaml
resources:
  - name: nginx-config
    type: file
    targetpath: /etc/nginx/nginx.conf
    source: templates/nginx/nginx.conf.tmpl
    mode: "0644"
```

Setting both `source` and `content` on the same resource is an error.

### Module-level dependencies

Modules can declare ordering relative to other modules:

```yaml
name: app-deploy
depends_on: [nginx]     # this module runs after nginx
# or equivalently:
require: [nginx]        # alias for depends_on

name: base-packages
before: [nginx]         # nginx runs after this module (reverse depends_on)
```

## Role Format

A role is a named list of modules. Module references can be plain strings or objects with a `when` expression.

```yaml
name: webserver
modules:
  - nginx
  - openssh
  - name: certbot
    when: "is_prod_host"
```

## Node Mapping Format

Node files map hostname patterns to roles and modules. First match wins -- order matters.

```yaml
nodes:
  - match: "web-*.prod.example.com"
    environment: production
    roles: [webserver]
    vars:
      port: "443"
    env_vars:
      production:
        worker_count: "auto"
      staging:
        worker_count: "2"

  - match: "*.example.com"
    roles: [base]
```

Multiple node files in the `nodes/` directory are merged in filesystem order. Duplicate `modules` and `roles` keys within a single entry are merged (not overwritten).

## Variable Precedence

Variables are merged in this order (last wins):

1. Module default vars (lowest priority)
2. Node-level vars
3. `env_vars` for the node's environment (highest priority)

Conditional vars (see [../conditionals/README.md](../conditionals/README.md)) take precedence over node-level vars of the same name.

## Template Syntax

Go templates with two namespaces:

- `.Vars` -- merged variables after precedence resolution
- `.Traits` -- the node's collected traits (discovered facts)

```
{{ .Vars.app_port }}
{{ .Traits.os.distro }}
{{ .Traits.network.hostname }}
{{ if eq .Vars.ssl_enabled "true" }}
  ssl_certificate /etc/ssl/certs/{{ .Traits.network.hostname }}.pem;
{{ end }}
```

Templates are rendered at check-in time (server-side), after variables and conditionals are resolved.

## Conditional Variables

Conditional vars are structured YAML maps with a `trait:` field and one operator. They evaluate to `"true"` or `"false"` and can be used in templates and resource filters.

```yaml
vars:
  is_debian:
    trait: os.family
    in: [debian]
  high_memory:
    trait: hardware.memory_mb
    gte: 4096
  is_prod_host:
    trait: os.hostname
    regex: ".*\\.prod\\..*"
```

Operators: `in`, `not_in`, `gt`, `lt`, `gte`, `lte`, `regex`. See [../conditionals/README.md](../conditionals/README.md) for full details.

## Secret Resolution

Values prefixed with `secret:` are resolved through the secrets provider at config load time:

```yaml
database:
  dsn: "secret:hobnob/db/dsn"
```

Known secret fields (`dsn`, `tls_cert_path`, `tls_key_path`, `webhook_secret`) are required to use the `secret:` prefix. Plain values for these fields cause a load error. Resolved secrets never appear in logs, API responses, or gRPC payloads.

## Resource Dependencies

| Keyword | Direction | Description |
|---------|-----------|-------------|
| `depends_on` | Forward | This resource runs after the listed resources |
| `require` | Forward | Alias for `depends_on` |
| `before` | Reverse | Listed resources run after this resource (resolved at load time) |
| `notify` | Trigger | Re-execute target resource if this one makes a change |
| `subscribes` | Trigger | Listen for changes on the named resource (converted to `notify` internally) |

Circular dependencies are detected and rejected at config load time.

## Conditional Execution

Conditional vars are evaluated on the agent against local traits and injected into the vars map as `"true"`/`"false"` strings. Resources use `when` expressions to conditionally execute:

```yaml
resources:
  - name: debian-only
    type: package
    package: apt-transport-https
    when: "is_debian"

  - name: high-perf-config
    type: file
    targetpath: /etc/app/performance.conf
    content: "threads: 16"
    when: "high_memory || many_cpus"

  - name: skip-on-staging
    type: exec
    command: /usr/local/bin/deploy
    when: "!is_staging"
```

## When Expressions

`when` expressions are evaluated by the agent at execution time. They can reference conditional var results and built-in functions for runtime checks:

```yaml
resources:
  - name: weekday-task
    type: exec
    command: /usr/local/bin/weekday-task
    when: "day_of_week('monday') || day_of_week('tuesday')"

  - name: restart-if-running
    type: service
    service: myapp
    state: running
    when: "process_running('myapp')"
```

Built-in functions: `file_exists`, `dir_exists`, `command_succeeds`, `process_running`, `hour_range`, `day_of_week`, `day_of_month`, `flag_set`, `os_family`, `distro`, `arch`, `version_ge`, `in_group`.

## Conditional Blocks

Group resources under a shared `when` expression using `conditional_block`:

```yaml
resources:
  - conditional_block:
      when: "os_family('debian')"
      resources:
        - name: install-apt-transport
          type: package
          package: apt-transport-https
        - name: update-apt
          type: exec
          command: apt-get update
```

Block-level `when` is AND-composed with each child resource's individual `when` (if any).

## Hot-Reload

- fsnotify watches `modules/`, `roles/`, `nodes/`, and `templates/` directories recursively
- File changes trigger a full re-parse after a 500ms debounce window
- Policy version is incremented on each successful reload
- On parse failure, the previous valid config is retained and an error is logged
- Agents detect the version change on their next check-in and receive updated policy
