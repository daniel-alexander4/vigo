# Composition Patterns

## The Problem

Configuration management at scale creates repetition. You have 50 web servers that are mostly identical, 3 environments with slightly different settings, and cross-cutting concerns (monitoring, security, logging) that apply to everything. Without composition tools, you end up with hundreds of lines of copy-pasted YAML with minor variations — a maintenance burden where a single change requires editing dozens of files.

Other CM systems solve this with full programming languages (Puppet's DSL, Chef's Ruby, Ansible's Jinja2 + Python). These are powerful but sacrifice readability — an operator unfamiliar with the tool can't glance at a config and understand it.

Hobnob solves this differently: **layered composition in plain YAML**, with each layer handling a specific type of reuse. No DSL, no programming language, no templating for control flow. Every config file is readable by anyone who knows YAML.

## The Composition Stack

Hobnob provides five layers of composition, from broadest to most specific:

```
thrall glob match    → assigns roles/modules to groups of machines
  roles              → bundles modules into reusable profiles
    modules          → packages related resources together
      resource tools → foreach, case, conditional_block, when, defaults
        templates    → dynamic values from traits and vars
```

Each layer eliminates a specific type of repetition.

## Layer 1: Glob Matching — Group Machines by Name

**Problem:** 50 web servers need the same configuration.

**Solution:** One thrall entry with a glob pattern covers all of them.

```yaml
# thralls/web-servers.hnb
thralls:
  - match: "web*.prod.example.com"
    environment: production
    roles: [webserver]
    vars:
      workers: 16
```

This single entry covers `web01.prod.example.com` through `web99.prod.example.com`. First match wins — more specific patterns go first, catch-all `"*"` goes last.

**Split by environment with the same role:**

```yaml
thralls:
  - match: "web*.prod.example.com"
    environment: production
    roles: [webserver]
    vars:
      workers: 16
      ssl: true

  - match: "web*.staging.example.com"
    environment: staging
    roles: [webserver]
    vars:
      workers: 4
      ssl: false

  - match: "web*.dev.example.com"
    environment: development
    roles: [webserver]
    vars:
      workers: 1
      ssl: false
```

Three entries cover an entire multi-environment web fleet with different tuning per environment.

**Per-host overrides with `vars_from`:**

```yaml
thralls:
  - match: "web*.prod.example.com"
    environment: production
    roles: [webserver]
    vars:
      workers: 16
    vars_from:
      - "vars/web-common.hnb"
      - "vars/{{ .Hostname }}.hnb"  # host-specific overrides, skipped if missing
```

This gives every web server the same base config, with an optional per-host override file for snowflakes.

## Layer 2: Roles — Bundle Modules into Profiles

**Problem:** A "webserver" needs nginx, logrotate, monitoring, and security hardening. Listing all four modules in every thrall entry is repetitive.

**Solution:** Define a role once, assign it everywhere.

```yaml
# roles/webserver.hnb
name: webserver
modules:
  - nginx
  - logrotate
  - node-exporter
  - name: security-hardening
    when: "!is_container"
```

Now thrall entries just say `roles: [webserver]`.

**Role includes for shared foundations:**

```yaml
# roles/base.hnb
name: base
modules:
  - ntp
  - sshd-config
  - sudo-config
  - log-forwarding

# roles/webserver.hnb
name: webserver
includes: [base]          # pulls in ntp, sshd, sudo, logging
modules:
  - nginx
  - logrotate

# roles/database.hnb
name: database
includes: [base]          # same foundation
modules:
  - postgres
  - pgbouncer
  - backup-agent
```

Every role that includes `base` gets the foundational modules. Change `base` once, it propagates to all roles.

**Conditional modules in roles:**

```yaml
name: webserver
includes: [base]
modules:
  - nginx
  - logrotate
  - name: node-exporter
    when: "!is_container"       # skip monitoring agent in containers
  - name: crowdsec
    when: "os_family('debian')" # security agent only on Debian
```

## Layer 3: Modules — Package Related Resources

**Problem:** Installing nginx requires a package, a config file, and a service, all wired together with dependencies and notifications.

**Solution:** A module bundles these into a single reusable unit with configurable variables.

```yaml
# modules/nginx.hnb
name: nginx

vars:
  nginx_port: 80
  worker_connections: 1024
  server_names: [localhost]

resources:
  - name: nginx-package
    type: package
    package: nginx

  - name: nginx-config
    type: file
    targetpath: /etc/nginx/nginx.conf
    source: templates/nginx.conf.tmpl
    depends_on: [nginx-package]
    notify: [nginx-service]

  - name: nginx-service
    type: service
    service: nginx
    state: running
    enabled: true
    depends_on: [nginx-package]
```

Thrall entries override the module's default vars:

```yaml
thralls:
  - match: "web*.prod.example.com"
    roles: [webserver]
    vars:
      nginx_port: 443
      worker_connections: 4096
      server_names: [app.example.com, api.example.com]
```

The module definition stays clean. Only the values that differ are overridden at the node level.

**Module-level `defaults` for shared attributes:**

```yaml
name: app-configs

defaults:
  owner: deploy
  group: deploy
  mode: "0640"

resources:
  - name: app-config
    type: file
    targetpath: /etc/myapp/config.yaml
    content: "..."
    # owner, group, mode inherited from defaults

  - name: db-config
    type: file
    targetpath: /etc/myapp/database.yaml
    content: "..."
    # also inherits defaults
```

**Module ordering with `depends_on` and `before`:**

```yaml
# modules/app.hnb
name: app
depends_on: [nginx, postgres]   # app module runs after nginx and postgres modules

resources:
  - name: deploy-app
    type: exec
    command: "/usr/local/bin/deploy.sh"
```

## Layer 4: Resource Tools — Eliminate Per-Resource Repetition

### foreach — Generate N resources from one definition

**Problem:** Manage 5 nginx vhosts with the same structure.

```yaml
vars:
  vhosts:
    - name: app
      domain: app.example.com
      root: /var/www/app
    - name: api
      domain: api.example.com
      root: /var/www/api
    - name: docs
      domain: docs.example.com
      root: /var/www/docs

resources:
  - name: "vhost-{{ .Item.name }}"
    type: file
    foreach: vhosts
    targetpath: "/etc/nginx/sites-enabled/{{ .Item.name }}.conf"
    content: |
      server {
          server_name {{ .Item.domain }};
          root {{ .Item.root }};
      }
    notify: [reload-nginx]

  - name: "webroot-{{ .Item.name }}"
    type: directory
    foreach: vhosts
    targetpath: "{{ .Item.root }}"
    owner: www-data
```

Two resource definitions expand to 6 resources (3 config files + 3 directories). Adding a vhost means adding one entry to the `vhosts` var — zero resource duplication.

### case — Switch resource attributes by value

**Problem:** Package manager config differs by OS family.

```yaml
resources:
  - name: package-manager-config
    type: file
    case: "{{ .Traits.os.family }}"
    match:
      debian:
        targetpath: /etc/apt/apt.conf.d/99-hobnob
        content: |
          APT::Install-Recommends "false";
      redhat:
        targetpath: /etc/yum.conf.d/hobnob.conf
        content: |
          installonly_limit=3
```

One resource definition, two behaviors. The `case` expression selects which `match` branch applies.

**Combined with traits for hardware-adaptive config:**

```yaml
resources:
  - name: jvm-heap
    type: file
    targetpath: /etc/app/jvm.opts
    case: "{{ if ge .Traits.hardware.memory_mb 32768 }}large{{ else if ge .Traits.hardware.memory_mb 8192 }}medium{{ else }}small{{ end }}"
    match:
      large:
        content: "-Xmx16g -XX:+UseG1GC"
      medium:
        content: "-Xmx4g -XX:+UseG1GC"
      small:
        content: "-Xmx512m -XX:+UseSerialGC"
```

The agent's actual RAM determines the JVM configuration — no per-host overrides needed.

### conditional_block — Group resources under a shared condition

**Problem:** A block of Debian-specific resources clutters the module with repeated `when:` guards.

```yaml
resources:
  - conditional_block:
      when: "os_family('debian')"
      resources:
        - name: apt-transport-https
          type: package
          package: apt-transport-https

        - name: ca-certificates
          type: package
          package: ca-certificates

        - name: apt-update
          type: exec
          command: "apt-get update -qq"
          creates: /var/lib/apt/lists/lock

  - conditional_block:
      when: "os_family('redhat')"
      resources:
        - name: epel-release
          type: package
          package: epel-release
```

The `when:` is written once per block, not once per resource.

### when — Conditional individual resources

**Problem:** Some resources only make sense on certain platforms or configurations.

```yaml
resources:
  - name: enable-firewall
    type: service
    service: ufw
    state: running
    enabled: true
    when: "os_family('debian') && !is_container"

  - name: install-selinux-tools
    type: package
    package: policycoreutils-python-utils
    when: "os_family('redhat') && has_selinux"
```

### environment_overrides — Per-environment variable overrides

**Problem:** Production, staging, and dev need different values for the same variables.

```yaml
thralls:
  - match: "app*.example.com"
    roles: [app-server]
    vars:
      replicas: 1
      log_level: info
      debug: false
    environment_overrides:
      production:
        replicas: 3
        log_level: warn
      staging:
        log_level: debug
        debug: true
      development:
        log_level: trace
        debug: true
```

Base vars provide sensible defaults. Each environment overrides only what differs.

## Layer 5: Templates — Dynamic Values from Traits

**Problem:** Config file content needs to reference the machine's actual hostname, IP, CPU count, etc.

```yaml
resources:
  - name: prometheus-config
    type: file
    targetpath: /etc/prometheus/prometheus.yml
    content: |
      global:
        scrape_interval: 15s
        external_labels:
          host: {{ .Traits.hostname }}
          environment: {{ .Vars.environment }}

      scrape_configs:
        - job_name: node
          static_configs:
            - targets: ['localhost:{{ .Vars.exporter_port }}']
```

Templates resolve at check-in time using the thrall's actual traits and resolved vars. They're only allowed in `content:` attributes and `source:` template files — not in `command:`, `targetpath:`, or other parameters.

## Putting It All Together

A complete example managing a web fleet across two environments:

```yaml
# roles/base.hnb
name: base
modules: [ntp, sshd-config, node-exporter]

# roles/webserver.hnb
name: webserver
includes: [base]
modules:
  - nginx
  - logrotate
  - name: crowdsec
    when: "os_family('debian')"

# modules/nginx.hnb
name: nginx
vars:
  nginx_port: 80
  vhosts: []
defaults:
  owner: root
  group: root
resources:
  - name: nginx-package
    type: package
    package: nginx
  - name: "vhost-{{ .Item.name }}"
    type: file
    foreach: vhosts
    targetpath: "/etc/nginx/sites-enabled/{{ .Item.name }}.conf"
    source: templates/nginx-vhost.tmpl
    notify: [reload-nginx]
  - name: reload-nginx
    type: exec
    command: "nginx -s reload"
    refreshonly: true
  - name: nginx-service
    type: service
    service: nginx
    state: running
    enabled: true

# thralls/web-fleet.hnb
thralls:
  - match: "web*.prod.example.com"
    environment: production
    roles: [webserver]
    vars:
      nginx_port: 443
      vhosts:
        - {name: app, domain: app.example.com}
        - {name: api, domain: api.example.com}
    vars_from: ["vars/{{ .Hostname }}.hnb"]

  - match: "web*.staging.example.com"
    environment: staging
    roles: [webserver]
    vars:
      nginx_port: 8080
      vhosts:
        - {name: app, domain: staging-app.example.com}
```

**What this achieves:**

- `base` role shared across webserver, database, and any future role
- `webserver` role composes base + nginx + logrotate + conditional security
- `nginx` module is fully parameterized — vhosts driven by a var list
- One thrall entry covers all production web servers
- Per-host overrides available via `vars_from` for snowflakes
- Templates inject actual hostnames, ports, and domains at check-in time

**No duplication.** Adding a new vhost: add one entry to the `vhosts` list. Adding a new environment: add one thrall entry with the environment name. Adding monitoring to all servers: add the module to `base` role. Each change happens in exactly one place.

## Related

- [Config Format](config-format.md) — Module, role, and thrall structure
- [Resource Language](resource-language.md) — foreach, case, conditional_block, defaults, stream_edit
- [When Expressions](when-expressions.md) — Conditional logic syntax
- [Templates](templates.md) — Go template expressions in content
- [Resource DAG](resource-dag.md) — Dependency and notification ordering
