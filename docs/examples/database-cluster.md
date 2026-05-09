
# Database Cluster

This example sets up PostgreSQL with managed credentials using `secret:` references, directory-level `common.vgo` for shared configuration, and `environments.vgo` for environment-specific tuning.

## PostgreSQL Module

`stockpile/modules/postgresql.vgo`:

```yaml
name: postgresql
vars:
  pg_version: "16"
  pg_port: "5432"
  pg_max_connections: "100"
  pg_shared_buffers: 256MB
  pg_effective_cache_size: 768MB
  pg_work_mem: 4MB
  pg_listen_addresses: localhost
resources:
  - name: pg-package
    type: package
    package: "postgresql-{{ .Vars.pg_version }}"
    state: present
    when: "os_family('debian')"

  - name: pg-package-rhel
    type: package
    package: "postgresql{{ .Vars.pg_version }}-server"
    state: present
    when: "!os_family('debian')"

  - name: pg-config
    type: file
    target_path: "/etc/postgresql/{{ .Vars.pg_version }}/main/postgresql.conf"
    owner: postgres
    group: postgres
    mode: "0644"
    content: |
      port = {{ .Vars.pg_port }}
      max_connections = {{ .Vars.pg_max_connections }}
      shared_buffers = {{ .Vars.pg_shared_buffers }}
      effective_cache_size = {{ .Vars.pg_effective_cache_size }}
      work_mem = {{ .Vars.pg_work_mem }}
      listen_addresses = '{{ .Vars.pg_listen_addresses }}'
      ssl = on
      ssl_cert_file = '/etc/ssl/certs/ssl-cert-snakeoil.pem'
      ssl_key_file = '/etc/ssl/private/ssl-cert-snakeoil.key'
      log_destination = 'syslog'
      logging_collector = off
    notify:
      - pg-service

  - name: pg-hba
    type: file
    target_path: "/etc/postgresql/{{ .Vars.pg_version }}/main/pg_hba.conf"
    owner: postgres
    group: postgres
    mode: "0640"
    content: |
      # TYPE  DATABASE  USER      ADDRESS         METHOD
      local   all       postgres                  peer
      local   all       all                       peer
      host    all       all       127.0.0.1/32    scram-sha-256
      host    all       all       ::1/128         scram-sha-256
      host    all       all       {{ .Vars.pg_allowed_cidr }}  scram-sha-256
    notify:
      - pg-service

  - name: pg-service
    type: service
    service: postgresql
    state: running
    enabled: true
```

## Application Database Setup

`stockpile/modules/app-database.vgo`:

```yaml
name: app-database
depends_on:
  - postgresql
resources:
  - name: create-app-db
    type: exec
    command: "psql -c \"SELECT 1 FROM pg_database WHERE datname='{{ .Vars.app_db_name }}'\" | grep -q 1 || createdb {{ .Vars.app_db_name }}"
    user: postgres
    unless: "psql -lqt | cut -d \\| -f 1 | grep -qw {{ .Vars.app_db_name }}"

  - name: create-app-user
    type: exec
    command: "psql -c \"DO \\$\\$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='{{ .Vars.app_db_user }}') THEN CREATE ROLE {{ .Vars.app_db_user }} LOGIN; END IF; END \\$\\$;\""
    user: postgres
    unless: "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname='{{ .Vars.app_db_user }}'\" | grep -q 1"

  - name: set-app-user-password
    type: exec
    stdin: "secret:vigo/app/db_password"
    command: "psql -c \"ALTER ROLE {{ .Vars.app_db_user }} PASSWORD '$(cat -)';\""
    user: postgres
    watch_secret: ["vigo/app/db_password"]
    when: "changed"

  - name: grant-app-privileges
    type: exec
    command: "psql -c \"GRANT ALL PRIVILEGES ON DATABASE {{ .Vars.app_db_name }} TO {{ .Vars.app_db_user }};\""
    user: postgres
```

## Shared Variables via common.vgo

Directory-level `common.vgo` files set shared vars for every envoy in the subtree. A `common.vgo` alongside the database envoys provides the database-wide defaults:

`stockpile/db/common.vgo`:

```yaml
vars:
  app_db_name: myapp
  app_db_user: myapp
  pg_allowed_cidr: "10.0.0.0/8"
```

Any match block under `stockpile/db/` inherits these vars; match-block vars override them.

## Role Definition

`stockpile/roles/database-server.vgo`:

```yaml
name: database-server
modules:
  - postgresql
  - app-database
```

## Node Assignment

`stockpile/db/envoys.vgo`:

```yaml
envoys:
  - match: "db-prod-*.example.com"
    environment: production
    roles: [database-server]
    vars:
      pg_listen_addresses: "*"

  - match: "db-staging-*.example.com"
    environment: staging
    roles: [database-server]
    vars:
      pg_listen_addresses: "*"
```

Match blocks stay small — only vars that aren't env-specific go here.

## Per-Environment Tuning

`stockpile/db/environments.vgo`:

```yaml
env:
  production:
    vars:
      pg_max_connections: "200"
      pg_shared_buffers: 4GB
      pg_effective_cache_size: 12GB
      pg_work_mem: 16MB

  staging:
    vars:
      pg_max_connections: "50"
      pg_shared_buffers: 512MB
      pg_effective_cache_size: 1536MB
      pg_work_mem: 4MB
```

Env-specific tuning lives in one file, keyed by tier. Adding a new tier means adding one block here — no touching the match blocks.

## Secret References

The `secret:` prefix in the `stdin` parameter of the `set-app-user-password` resource means:

1. The server resolves `secret:vigo/app/db_password` through the secrets provider at check-in time
2. The resolved value is passed to the agent in the policy bundle
3. The agent pipes the value to the command's stdin
4. The password never appears in command strings, logs, or config files

For the `local` secrets backend, create the secret file:

```bash
mkdir -p /srv/vigo/secrets/vigo/app
longdrawer -n 'my-secure-password' > /srv/vigo/secrets/vigo/app/db_password
chmod 600 /srv/vigo/secrets/vigo/app/db_password
```

## Summary of mechanisms used

- `common.vgo` — shared vars for every envoy in a subtree (DRY across match blocks)
- Match-block `vars:` — vars that differ per match pattern
- `environments.vgo` — vars that differ per tier (`environment:`)
- `secret:` prefix — credentials resolved server-side at check-in

See [Multi-Axis Config](../engineering/writing-modules/multi-axis-config.md) for the full canonical model.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
