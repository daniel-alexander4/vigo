
# Multi-Environment Setup

This example shows how to maintain different variable values for production and staging while sharing the same configcrates. Vigo's canonical mechanism for env-specific values is `environments.vgo` at the appropriate directory level — env is declared on each match block, and env-specific vars are centralized in a sibling file.

## Configcrate Definition

`stacks/configcrates/webapp.vgo`:

```yaml
name: webapp
vars:
  app_port: "8080"
  app_workers: "2"
  app_log_level: info
  app_domain: localhost
  app_db_host: localhost
resources:
  - name: webapp-config
    type: file
    target_path: /etc/webapp/config.yaml
    owner: webapp
    group: webapp
    mode: "0640"
    content: |
      port: {{ .Vars.app_port }}
      workers: {{ .Vars.app_workers }}
      log_level: {{ .Vars.app_log_level }}
      domain: {{ .Vars.app_domain }}
      database:
        host: {{ .Vars.app_db_host }}
        password: {{ .Vars.app_db_password }}
    notify:
      - webapp-service

  - name: webapp-service
    type: service
    service: webapp
    state: running
    enabled: true
```

## Match Blocks Declare Environment

`stacks/envoys.vgo`:

```yaml
envoys:
  - match: "prod-*.web.example.com"
    environment: production
    roles: [webserver]

  - match: "stg-*.web.example.com"
    environment: staging
    roles: [webserver]
```

Each match block declares the tier via `environment:`. No inline env-specific vars on the match block itself.

## Per-Environment Overrides

`stacks/environments.vgo` (sibling to `envoys.vgo`, or at any ancestor directory):

```yaml
env:
  production:
    vars:
      app_port: "8080"
      app_workers: "8"
      app_log_level: warn
      app_domain: example.com
      app_db_host: db-prod.internal
      app_db_password: "secret:vigo/webapp/prod/db_password"

  staging:
    vars:
      app_port: "8080"
      app_workers: "2"
      app_log_level: debug
      app_domain: staging.example.com
      app_db_host: db-staging.internal
      app_db_password: "secret:vigo/webapp/staging/db_password"
```

## How It Resolves

At check-in time the server:

1. Matches the envoy hostname against `envoys.vgo` (first match wins) to determine role + `environment:`.
2. Expands roles to a configcrate list and loads configcrate defaults.
3. Applies `common.vgo` inherited vars (parent → child) on top of configcrate defaults.
4. Applies match-block vars (if any — in this example there are none).
5. Applies `environments.vgo` vars for the block's `environment:` on top.

`environments.vgo` is scoped by filesystem location: a file at `stacks/customerA/environments.vgo` only affects envoys whose hostcrate lives at `stacks/customerA/` or deeper.

## Separate Secrets per Environment

Because the database password lives in `environments.vgo` too, each env naturally gets its own secret path — no per-match-block duplication needed. The secrets provider resolves the `secret:` prefix at check-in time.
