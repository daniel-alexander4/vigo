# Multi-Environment Setup

This example shows how to use `env_vars` to maintain different variable values for production and staging environments while sharing the same modules.

## Module Definition

`thrall-configs/modules/webapp.hnb`:

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
    targetpath: /etc/webapp/config.yaml
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
    ensure: running
    enable: true
```

## Node Assignment with env_vars

`thrall-configs/nodes.hnb`:

```yaml
thralls:
  - match: "*.web.example.com"
    roles: [webserver]
    vars:
      app_db_password: "secret:hobnob/webapp/db_password"
    env_vars:
      production:
        app_port: "8080"
        app_workers: "8"
        app_log_level: warn
        app_domain: example.com
        app_db_host: db-prod.internal
      staging:
        app_port: "8080"
        app_workers: "2"
        app_log_level: debug
        app_domain: staging.example.com
        app_db_host: db-staging.internal
```

## How env_vars Works

1. The `vars` block sets base values that apply to all environments.
2. The `env_vars` block defines per-environment overrides.
3. A thrall's environment is set in its node entry:

```yaml
thralls:
  - match: "prod-*.web.example.com"
    environment: production
    roles: [webserver]

  - match: "stg-*.web.example.com"
    environment: staging
    roles: [webserver]
```

4. At check-in time, the server resolves the thrall's environment and merges:
   - Module default vars (lowest priority)
   - Node-level `vars` (overrides module defaults)
   - `env_vars[environment]` (highest priority)

## Promoting Between Environments

Use `hobnobcli env promote` to promote variable values from one environment to another:

```bash
hobnobcli env promote --module webapp --from staging --to production
```

This shows the diff between environments and creates an audit record.

## Separate Secrets per Environment

Different environments can reference different secrets:

```yaml
thralls:
  - match: "prod-*.web.example.com"
    environment: production
    vars:
      app_db_password: "secret:hobnob/webapp/prod/db_password"

  - match: "stg-*.web.example.com"
    environment: staging
    vars:
      app_db_password: "secret:hobnob/webapp/staging/db_password"
```

Each environment gets its own secret path, resolved by the secrets provider at check-in time.
