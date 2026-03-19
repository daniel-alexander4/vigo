# thrall-configs

Configuration files that define the desired state of managed thralls. These YAML files (with `.hnb` extension) are the single source of truth -- no config is written via API, CLI, or UI.

## Summary

- YAML syntax with `.hnb` file extension
- Three concepts: modules (reusable resource sets), roles (module groups), thrall entries (hostname-to-role mapping)
- Directory structure defines inheritance via `common.hnb` files
- Single source of truth: edit files, publish to reload the server
- Go template syntax with `.Traits` and `.Vars` namespaces
- Secret references use `secret:` prefix, resolved at server load time

## Directory Structure

```
thrall-configs/
  common.hnb            # Modules/vars inherited by ALL thrall entries
  roles.hnb             # All role definitions in one file
  modules/              # Reusable resource definitions
    nginx.hnb
    openssh.hnb
  templates/            # Template files referenced by source:
  default/
    all.hnb             # Thrall entries (match: patterns -> roles/modules)
  production/
    common.hnb          # Extra modules/vars for all prod thralls
    web/
      web.hnb           # match: "web*.prod.example.com"
  staging/
    common.hnb          # match: "*.staging.example.com"
```

Module subdirectories are supported for organization:

```
modules/
  web/
    nginx.hnb
    haproxy.hnb
  security/
    openssh.hnb
    fail2ban.hnb
```

## Quickstart

1. Copy the example files:

```bash
cp common.hnb.example common.hnb
cp roles.hnb.example roles.hnb
mkdir -p default
cp default/all.hnb.example default/all.hnb
```

2. Edit `common.hnb` to set modules every machine gets:

```yaml
modules: [standard-users, diag-tools]

vars:
  org_name: "My Company"
```

3. Edit `default/all.hnb` to map hostnames to roles/modules:

```yaml
thralls:
  - match: "web-*.example.com"
    roles: [webserver]

  - match: "*.example.com"
    modules: [monitoring]
```

4. The server detects changes automatically -- no restart needed.

## File Types

### common.hnb (inheritance)

A `common.hnb` at any directory level defines modules, roles, and vars inherited by all thrall entries in that directory and its subdirectories. No `match:` or `thralls:` keys.

```yaml
modules: [standard-users, diag-tools]

vars:
  ntp_server: "pool.ntp.org"
```

Root-level `common.hnb` applies to entries in subdirectories only. A `common.hnb` in `production/` applies to all entries under `production/`.

### roles.hnb (role definitions)

Single file at the root. Groups modules into named profiles.

```yaml
roles:
  - name: webserver
    includes: [bastion]
    modules: [nginx, certbot]

  - name: bastion
    modules: [openssh, fail2ban]
```

### Thrall entry files (leaf files with match:)

Any `.hnb` file that is not `common.hnb` or `roles.hnb`. Must have `thralls:` with `match:` entries.

```yaml
thralls:
  - match: "web-*.prod.example.com"
    environment: production
    roles: [webserver]
    vars:
      worker_count: "auto"

  - match: "db-*.example.com"
    roles: [database]
    modules: [monitoring]
```

### Module files (modules/)

Reusable resource definitions with optional default vars.

```yaml
name: openssh

vars:
  ssh_port: "22"

resources:
  - name: install-openssh
    type: package
    package: openssh-server
    state: present

  - name: sshd-config
    type: file
    target_path: /etc/ssh/sshd_config
    content: |
      Port {{ .Vars.ssh_port }}
    mode: "0600"
    owner: root
    group: root
    depends_on: [install-openssh]
    notify: [sshd-service]

  - name: sshd-service
    type: service
    service: sshd
    state: running
    enabled: true
```

## Inheritance

Directory structure defines what modules/vars thrall entries inherit:

```
common.hnb              -> modules: [standard-users]     # ALL entries get this
production/
  common.hnb            -> modules: [monitoring]          # prod entries also get this
  web/
    web.hnb             -> match: "web*.prod.example.com" # inherits standard-users + monitoring
```

- Parent modules come before child modules in the DAG
- Child vars override parent vars
- `exclude_modules:` allows entries to opt out of inherited modules

## Variable Precedence

1. Module default vars (lowest priority)
2. Inherited vars from `common.hnb` (root to leaf order)
3. Thrall-level vars
4. `environment_overrides` for the thrall's environment (highest priority)

## Templates

Go template syntax is available in `content:` attributes:

```yaml
content: |
  server_name {{ .Traits.network.hostname }};
  listen {{ .Vars.port }};
```

Available namespaces: `.Vars` (merged variables), `.Traits` (collected traits).

## Secrets

Reference secrets with the `secret:` prefix -- resolved by the server at config load time:

```yaml
vars:
  db_dsn: "secret:myapp/db/dsn"
```

## Dependencies and Triggers

```yaml
resources:
  - name: config-file
    type: file
    target_path: /etc/nginx/nginx.conf
    content: "..."
    depends_on: [install-pkg]
    notify: [restart-svc]
```

Aliases: `require` = `depends_on`. `before` = reverse `depends_on`.

## Conditional Execution

```yaml
  - name: apt-setup
    type: exec
    command: apt-get update
    when: "os_family('debian')"
```

## Validation

```bash
hobnobcli validate modules/nginx.hnb
hobnobcli vi modules/nginx.hnb
```
