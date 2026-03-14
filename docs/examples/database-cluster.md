# Database Cluster

This example sets up PostgreSQL with managed credentials using `secret:` references and `vars_from` for shared configuration.

## PostgreSQL Module

`thrall-configs/modules/postgresql.hnb`:

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
    ensure: present
    when: "os_family('debian')"

  - name: pg-package-rhel
    type: package
    package: "postgresql{{ .Vars.pg_version }}-server"
    ensure: present
    when: "!os_family('debian')"

  - name: pg-config
    type: file
    targetpath: "/etc/postgresql/{{ .Vars.pg_version }}/main/postgresql.conf"
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
    targetpath: "/etc/postgresql/{{ .Vars.pg_version }}/main/pg_hba.conf"
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
    ensure: running
    enable: true
```

## Application Database Setup

`thrall-configs/modules/app-database.hnb`:

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
    stdin: "secret:hobnob/app/db_password"
    command: "psql -c \"ALTER ROLE {{ .Vars.app_db_user }} PASSWORD '$(cat -)';\""
    user: postgres

  - name: grant-app-privileges
    type: exec
    command: "psql -c \"GRANT ALL PRIVILEGES ON DATABASE {{ .Vars.app_db_name }} TO {{ .Vars.app_db_user }};\""
    user: postgres
```

## Shared Variables with vars_from

Create a shared variable file for database credentials used across multiple node entries.

`thrall-configs/db-vars.hnb`:

```yaml
app_db_name: myapp
app_db_user: myapp
pg_allowed_cidr: "10.0.0.0/8"
```

## Role Definition

`thrall-configs/roles/database-server.hnb`:

```yaml
name: database-server
modules:
  - postgresql
  - app-database
```

## Node Assignment

`thrall-configs/nodes.hnb`:

```yaml
thralls:
  - match: "db-*.example.com"
    environment: production
    roles: [database-server]
    vars_from: db-vars.hnb
    vars:
      pg_max_connections: "200"
      pg_shared_buffers: 2GB
      pg_effective_cache_size: 6GB
      pg_work_mem: 16MB
      pg_listen_addresses: "*"
    env_vars:
      production:
        pg_max_connections: "200"
        pg_shared_buffers: 4GB
        pg_effective_cache_size: 12GB
      staging:
        pg_max_connections: "50"
        pg_shared_buffers: 512MB
        pg_effective_cache_size: 1536MB
```

## Secret References

The `secret:` prefix in the `stdin` parameter of the `set-app-user-password` resource means:

1. The server resolves `secret:hobnob/app/db_password` through the secrets provider at check-in time
2. The resolved value is passed to the agent in the policy bundle
3. The agent pipes the value to the command's stdin
4. The password never appears in command strings, logs, or config files

For the `local` secrets backend, create the secret file:

```bash
mkdir -p /etc/hobnob-server/secrets/hobnob/app
echo -n 'my-secure-password' > /etc/hobnob-server/secrets/hobnob/app/db_password
chmod 600 /etc/hobnob-server/secrets/hobnob/app/db_password
```

## vars_from

The `vars_from: db-vars.hnb` directive loads variables from a separate file, keeping shared configuration DRY across multiple node entries. Variables from `vars_from` have lower priority than inline `vars`, so they can be overridden per-node.
