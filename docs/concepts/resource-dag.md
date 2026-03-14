# Resource DAG

Resources within a module form a Directed Acyclic Graph (DAG) based on their dependency declarations. The agent executes resources in topological order — dependencies first, dependents after.

## depends_on

Declares that a resource must run after its dependencies succeed:

```yaml
resources:
  - name: nginx-package
    type: package
    package: nginx

  - name: nginx-config
    type: file
    targetpath: /etc/nginx/nginx.conf
    content: "..."
    depends_on: [nginx-package]

  - name: nginx-service
    type: service
    service: nginx
    state: running
    depends_on: [nginx-package, nginx-config]
```

Execution order: `nginx-package` → `nginx-config` → `nginx-service`.

If a dependency fails, all dependent resources are skipped.

## before

The inverse of `depends_on`. Declares that other resources must run after this one:

```yaml
resources:
  - name: repo-setup
    type: repository
    name: nginx-repo
    url: "https://nginx.org/packages/debian"
    before: [nginx-package]

  - name: nginx-package
    type: package
    package: nginx
```

`before: [nginx-package]` is equivalent to adding `depends_on: [repo-setup]` to `nginx-package`.

## notify

Triggers re-application of another resource when this one makes a change:

```yaml
resources:
  - name: nginx-config
    type: file
    targetpath: /etc/nginx/nginx.conf
    content: "..."
    notify: [nginx-service]

  - name: nginx-service
    type: service
    service: nginx
    state: running
```

Key behavior:
- `nginx-service` is only re-applied (restarted) when `nginx-config` reports a change
- If the config file is unchanged, the service is not touched
- Multiple resources can notify the same target — it's only re-applied once per run
- The notified resource must also be defined in the same module

```
┌──────────────┐    change     ┌──────────────┐
│ nginx-config │──── notify ──>│nginx-service  │
│ (file)       │               │(restart)      │
└──────────────┘               └──────────────┘
         │                            ▲
         │ no change                  │
         └── skip notify ─────── (not restarted)
```

## subscribes

The inverse of `notify`. A resource watches another and re-applies itself when the watched resource changes:

```yaml
resources:
  - name: app-config
    type: file
    targetpath: /etc/app/config.yaml
    content: "..."

  - name: app-service
    type: service
    service: app
    state: running
    subscribes: [app-config]
```

This is equivalent to `app-config` having `notify: [app-service]`.

## watch_secret

Watches secret paths for rotation. When the server detects a secret has been rotated, resources subscribing to that secret path are re-applied:

```yaml
resources:
  - name: app-config
    type: file
    targetpath: /etc/app/config.yaml
    content: |
      dsn: {{ .Vars.db_dsn }}
    watch_secret: ["hobnob/db/dsn"]
    notify: [app-service]
```

When the secret at `hobnob/db/dsn` is rotated:
1. The watcher detects the version change
2. The config file is re-rendered with the new secret value
3. The service is restarted via notify

## Module-Level Dependencies

Modules can depend on other modules:

```yaml
# modules/app.hnb
name: app
depends_on: [nginx, postgres]
resources:
  - ...
```

Module ordering ensures all resources in `nginx` and `postgres` run before any resource in `app`.

## Circular Dependency Detection

Circular dependencies are rejected at config load time:

```yaml
# This will fail validation:
resources:
  - name: a
    type: exec
    command: "echo a"
    depends_on: [b]

  - name: b
    type: exec
    command: "echo b"
    depends_on: [a]
```

Error: `circular dependency detected: a → b → a`

## Failure Propagation

```
┌───┐     ┌───┐     ┌───┐
│ A │────>│ B │────>│ C │
└───┘     └───┘     └───┘
            │
            ▼ (A fails)
          SKIPPED
            │
            ▼
          C SKIPPED
```

When a resource fails:
1. All resources that `depends_on` the failed resource are skipped
2. Skipping cascades through the dependency chain
3. Resources with no dependency on the failed resource continue normally

## Related

- [Config Format](config-format.md) — Module and resource structure
- [Resource Language](resource-language.md) — defaults, foreach, case/match
- [Executors](../executors/_index.md) — Available resource types
