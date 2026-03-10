# node-configs

Configuration files that define the desired state of managed nodes. These YAML files (with `.hnb` extension) are the single source of truth -- no config is written via API, CLI, or UI.

## Summary

- YAML syntax with `.hnb` file extension
- Three layers: modules (reusable resource sets), roles (module groups), nodes (hostname-to-role mapping)
- Single source of truth: edit files, server auto-reloads via fsnotify
- Go template syntax with `.Traits` and `.Vars` namespaces
- Secret references use `secret:` prefix, resolved at server load time

## Directory Structure

```
node-configs/
  modules/           # Reusable resource definitions
    nginx.hnb
    openssh.hnb
    monitoring.hnb
  roles/             # Named collections of modules
    webserver.hnb
    base.hnb
  nodes.hnb          # Map hostnames to roles and modules
```

Directories are read recursively -- subdirectories are supported for organization:

```
modules/
  web/
    nginx.hnb
    haproxy.hnb
  security/
    openssh.hnb
    fail2ban.hnb
  monitoring/
    prometheus.hnb
```

## Quickstart

1. Copy the example files to create your own configs:

```bash
cp modules/openssh.hnb.example modules/openssh.hnb
cp roles/webserver.hnb.example roles/webserver.hnb
cp nodes.hnb.example nodes.hnb
```

2. Edit `nodes.hnb` to map your hostnames to roles:

```yaml
nodes:
  - match: "web-*.example.com"
    roles: [webserver]

  - match: "*.example.com"
    roles: [base]
```

3. The server detects changes automatically -- no restart needed.

4. Validate your configs before deploying:

```bash
hobnobcli validate modules/openssh.hnb
```

## Examples

### Module File

A module defines a set of resources with optional default variables:

```yaml
name: openssh

vars:
  ssh_port: "22"
  ssh_permit_root: "no"

resources:
  - name: install-openssh
    type: package
    package: openssh-server
    state: present

  - name: sshd-config
    type: file
    targetpath: /etc/ssh/sshd_config
    content: |
      Port {{ .Vars.ssh_port }}
      PermitRootLogin {{ .Vars.ssh_permit_root }}
      PasswordAuthentication no
      ChallengeResponseAuthentication no
      UsePAM yes
    mode: "0600"
    owner: root
    depends_on: [install-openssh]
    notify: [sshd-service]

  - name: sshd-service
    type: service
    service: sshd
    state: running
    enabled: true
```

### Role File

A role groups modules together:

```yaml
name: webserver
modules:
  - nginx
  - openssh
  - fail2ban
```

### Node Mapping

Map hostnames (globs supported) to roles, modules, and variables. First match wins -- put specific patterns before broad ones.

```yaml
nodes:
  - match: "web-*.prod.example.com"
    environment: production
    roles: [webserver]
    vars:
      ssh_port: "2222"
    env_vars:
      production:
        worker_count: "auto"
      staging:
        worker_count: "2"

  - match: "db-*.example.com"
    roles: [database]
    modules: [monitoring]

  - match: "*.example.com"
    roles: [base]
```

## Variable Precedence

1. Module default vars (lowest priority)
2. Node-level vars
3. env_vars for the node's environment (highest priority)

## Templates

Go template syntax is available in resource attributes:

```yaml
content: |
  server_name {{ .Traits.network.hostname }};
  listen {{ .Vars.port }};
  worker_processes {{ .Vars.worker_count }};
  # OS: {{ .Traits.os.distro }} {{ .Traits.os.version }}
```

Available namespaces:

- `.Vars` -- merged variables (module defaults + node overrides + env_vars)
- `.Traits` -- node's collected traits (os, hardware, network, etc.)

## Conditional Variables

Define variables whose value depends on a node's traits:

```yaml
vars:
  is_debian:
    trait: os.family
    in: [debian]

  has_gpu:
    trait: gpu.count
    gt: 0

  is_prod:
    trait: os.hostname
    regex: ".*\\.prod\\..*"
```

Results are `"true"` or `"false"` strings. Use them in conditional fields or templates.

## Dependencies and Triggers

```yaml
resources:
  - name: install-pkg
    type: package
    package: nginx
    state: present

  - name: config-file
    type: file
    targetpath: /etc/nginx/nginx.conf
    content: "..."
    depends_on: [install-pkg]    # runs after install-pkg
    notify: [restart-svc]        # triggers restart-svc if changed

  - name: restart-svc
    type: service
    service: nginx
    state: restarted
    subscribes: [config-file]    # also listens for config-file changes
```

Aliases: `require` is equivalent to `depends_on`. `before` is reverse `depends_on`.

## Conditional Execution

Conditional vars are evaluated on the agent and available as `"true"`/`"false"` in `when` expressions:

```yaml
  - name: apt-setup
    type: exec
    command: apt-get update
    when: "is_debian"
```

Built-in functions for runtime checks:

```yaml
  - name: weekday-task
    type: exec
    command: /usr/local/bin/daily-report
    when: "day_of_week('monday') && hour_range('9','17')"
```

## Secrets

Reference secrets with the `secret:` prefix -- resolved by the server at config load time:

```yaml
vars:
  db_dsn: "secret:myapp/db/dsn"

resources:
  - name: db-config
    type: file
    targetpath: /etc/app/db.conf
    content: |
      dsn={{ .Vars.db_dsn }}
```

Secrets never appear in plaintext in logs, API responses, or gRPC payloads.

## Validation

```bash
# Validate a config file
hobnobcli validate modules/nginx.hnb

# Edit with auto-validation on save
hobnobcli vi modules/nginx.hnb
```
