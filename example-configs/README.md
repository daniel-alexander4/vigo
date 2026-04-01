# examples

Configuration files that define the desired state of managed envoys. These YAML files (with `.vgo` extension) are the single source of truth -- no config is written via API, CLI, or UI.

## Summary

- YAML syntax with `.vgo` file extension
- Three concepts: modules (reusable resource sets), roles (module groups), envoy entries (hostname-to-role mapping)
- Directory structure defines inheritance via `common.vgo` files
- Single source of truth: edit files, publish to reload the server
- Go template syntax with `.Traits` and `.Vars` namespaces
- Secret references use `secret:` prefix, resolved at server load time

## Directory Structure

```
example-configs/
  common.vgo            # Modules/vars inherited by ALL envoy entries
  roles.vgo             # All role definitions in one file
  modules/              # Reusable resource definitions
    nginx.vgo
    openssh.vgo
  templates/            # Template files referenced by source:
  default/
    all.vgo             # Envoy entries (match: patterns -> roles/modules)
  production/
    common.vgo          # Extra modules/vars for all prod envoys
    web/
      web.vgo           # match: "web*.prod.example.com"
  staging/
    common.vgo          # match: "*.staging.example.com"
```

Module subdirectories are supported for organization:

```
modules/
  web/
    nginx.vgo
    haproxy.vgo
  security/
    openssh.vgo
    fail2ban.vgo
```

## Quickstart

1. Copy the example files:

```bash
cp common.vgo.example common.vgo
cp roles.vgo.example roles.vgo
mkdir -p default
cp default/all.vgo.example default/all.vgo
```

2. Edit `common.vgo` to set modules every machine gets:

```yaml
modules: [os-users, diag-tools]

vars:
  org_name: "My Company"
```

3. Edit `default/all.vgo` to map hostnames to roles/modules:

```yaml
envoys:
  - match: "web-*.example.com"
    roles: [webserver]

  - match: "*.example.com"
    modules: [monitoring]
```

4. The server detects changes automatically -- no restart needed.

## File Types

### common.vgo (inheritance)

A `common.vgo` at any directory level defines modules, roles, and vars inherited by all envoy entries in that directory and its subdirectories. No `match:` or `envoys:` keys.

```yaml
modules: [os-users, diag-tools]

vars:
  ntp_server: "pool.ntp.org"
```

Root-level `common.vgo` applies to entries in subdirectories only. A `common.vgo` in `production/` applies to all entries under `production/`.

### roles.vgo (role definitions)

Single file at the root. Groups modules into named profiles.

```yaml
roles:
  - name: webserver
    includes: [bastion]
    modules: [nginx, certbot]

  - name: bastion
    modules: [openssh, fail2ban]
```

### Envoy entry files (leaf files with match:)

Any `.vgo` file that is not `common.vgo` or `roles.vgo`. Must have `envoys:` with `match:` entries.

```yaml
envoys:
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

Directory structure defines what modules/vars envoy entries inherit:

```
common.vgo              -> modules: [os-users]     # ALL entries get this
production/
  common.vgo            -> modules: [monitoring]          # prod entries also get this
  web/
    web.vgo             -> match: "web*.prod.example.com" # inherits os-users + monitoring
```

- Parent modules come before child modules in the DAG
- Child vars override parent vars
- `exclude_modules:` allows entries to opt out of inherited modules

## Variable Precedence

1. Module default vars (lowest priority)
2. Inherited vars from `common.vgo` (root to leaf order)
3. Envoy-level vars
4. `environment_overrides` for the envoy's environment (highest priority)

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
vigocli validate modules/nginx.vgo
vigocli vi modules/nginx.vgo
```
