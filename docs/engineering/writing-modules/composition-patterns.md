
# Composition Patterns

## The Problem

Configuration management at scale creates repetition. You have 50 web servers that are mostly identical, 3 environments with slightly different settings, and cross-cutting concerns (monitoring, security, logging) that apply to everything. Without composition tools, you end up with hundreds of lines of copy-pasted YAML with minor variations — a maintenance burden where a single change requires editing dozens of files.

Other CM systems solve this with full programming languages (Puppet's DSL, Chef's Ruby, Ansible's Jinja2 + Python). These are powerful but sacrifice readability — an operator unfamiliar with the tool can't glance at a config and understand it.

Vigo solves this differently: **layered composition in plain YAML**, with each layer handling a specific type of reuse. No DSL, no programming language, no templating for control flow. Every config file is readable by anyone who knows YAML.

## The Composition Stack

Vigo provides six layers of composition, from broadest to most specific:

```
directory inheritance → shared base config via common.vgo at directory levels
  envoy glob match   → assigns roles/modules to groups of machines
    roles             → bundles modules into reusable profiles
      modules         → packages related resources together
        resource tools → foreach, case, conditional_block, when, defaults
          templates    → dynamic values from traits and vars
```

Each layer eliminates a specific type of repetition.

## Layer 0: Directory Inheritance — Shared Base Config

**Problem:** Every machine needs sshd, monitoring, and NTP. Every production machine also needs log shipping and audit logging. You don't want to repeat these on every envoy entry.

**Solution:** Place `common.vgo` files at directory levels. Everything in subdirectories inherits automatically.

```
stockpile/
├── common.vgo            # modules: [sshd, ntp, monitoring]
├── production/
│   ├── common.vgo        # modules: [log-shipping, auditd]
│   └── web/web.vgo       # match: "web*.prod.example.com", modules: [nginx]
└── staging/staging.vgo   # match: "*.staging.example.com", modules: [debug-tools]
```

A production web server gets: `sshd, ntp, monitoring, log-shipping, auditd, nginx`. A staging box gets: `sshd, ntp, monitoring, debug-tools`. Zero duplication.

Use `exclude_modules:` to opt out: a container host can drop `ntp` with `exclude_modules: [ntp]`.

See [Config Format: Directory Inheritance](config-format.md#directory-inheritance) for full details.

## Layer 1: Glob Matching — Group Machines by Name

**Problem:** 50 web servers need the same configuration.

**Solution:** One envoy entry with a glob pattern covers all of them.

```yaml
# envoys/web-servers.vgo
envoys:
  - match: "web*.prod.example.com"
    environment: production
    roles: [webserver]
    vars:
      workers: 16
```

This single entry covers `web01.prod.example.com` through `web99.prod.example.com`. First match wins — more specific patterns go first, catch-all `"*"` goes last.

**Split by environment with the same role:**

```yaml
envoys:
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

**Per-host variation via tags and lookup tables:**

```yaml
envoys:
  - match: "web*.prod.example.com"
    environment: production
    roles: [webserver]
    tags: [web, primary]
    vars:
      workers: 16
```

A per-host snowflake gets its own match block above the glob, or lookup-table entries keyed by hostname/tag inside the relevant module (see [Lookup Tables](lookups.md)). There is no per-host external var file — the 0.27.0 refactor removed `vars_from:` because directory inheritance + lookup tables already cover the same use cases without the extra file concept.

## Layer 2: Roles — Bundle Modules into Profiles

**Problem:** A "webserver" needs nginx, logrotate, monitoring, and security hardening. Listing all four modules in every envoy entry is repetitive.

**Solution:** Define a role once, assign it everywhere.

```yaml
# roles/webserver.vgo
name: webserver
modules:
  - nginx
  - logrotate
  - node-exporter
  - name: security-hardening
    when: "!is_container"
```

Now envoy entries just say `roles: [webserver]`.

**Role includes for shared foundations:**

```yaml
# roles/base.vgo
name: base
modules:
  - ntp
  - sshd-config
  - sudo-config
  - log-forwarding

# roles/webserver.vgo
name: webserver
includes: [base]          # pulls in ntp, sshd, sudo, logging
modules:
  - nginx
  - logrotate

# roles/database.vgo
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
# modules/nginx.vgo
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
    target_path: /etc/nginx/nginx.conf
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

Envoy entries override the module's default vars:

```yaml
envoys:
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
    target_path: /etc/myapp/config.yaml
    content: "..."
    # owner, group, mode inherited from defaults

  - name: db-config
    type: file
    target_path: /etc/myapp/database.yaml
    content: "..."
    # also inherits defaults
```

**Module ordering with `depends_on` and `before`:**

```yaml
# modules/app.vgo
name: app
depends_on: [nginx, postgres]   # app module runs after nginx and postgres modules

resources:
  - name: deploy-app
    type: exec
    command: "/usr/local/bin/deploy.sh"
```

### Module-level `foreach:` — Stamp Out a Module Per Item

**Problem:** A "vhost" isn't one resource — it's a docroot directory *plus* a config file *plus* a logrotate stanza *plus* a service reload. You have 8 of them, each with a different `server_name` and a couple of per-vhost knobs. Resource-level `foreach:` (Layer 4) only multiplies *one* resource at a time; you want to multiply the whole bundle.

**Solution:** Write the bundle once as a module, then reference it with `foreach:` — the module is instantiated once per item in a list var. Each item's fields are available throughout the module's resources as `{{ .Each.<field> }}` (or `{{ .Item }}` for a list of scalars).

```yaml
# modules/vhost.vgo — the "vhost type", written once
name: vhost
vars:
  doc_root_base: "/var/www"
resources:
  - name: vhost-docroot
    type: directory
    target_path: "{{ .Vars.doc_root_base }}/{{ .Each.server_name }}"
    owner: www-data
    mode: "0755"
  - name: vhost-conf
    type: file
    target_path: "/etc/nginx/sites-enabled/{{ .Each.server_name }}.conf"
    source: templates/nginx-vhost.tmpl     # the template sees .Each, .Vars, .Traits
    depends_on: [vhost-docroot]
    notify: [nginx-reload]                 # a resource in the (singleton) nginx module
  - name: vhost-logrotate
    type: file
    target_path: "/etc/logrotate.d/nginx-{{ .Each.server_name }}"
    content: |
      /var/log/nginx/{{ .Each.server_name }}.access.log { weekly rotate 8 compress }
```

```yaml
# roles/webserver.vgo — reference the module with foreach: + key:
name: webserver
modules:
  - nginx                       # singleton: package + service (defines nginx-reload)
  - name: vhost
    foreach: vhosts             # the var list to iterate
    key: server_name            # which item field names each instance
  - firewall-web
```

```yaml
# environments.vgo (or a common.vgo, or the match block) — the data
env:
  production:
    vars:
      vhosts:
        - server_name: www.example.com
        - server_name: shop.example.com
```

On a production web host this expands to module instances **`vhost[www.example.com]`** and **`vhost[shop.example.com]`**, each carrying its three resources with `{{ .Each.server_name }}` baked in. The module is written once; every host's set of vhosts is pure data, and it can live at whatever layer fits — a `common.vgo` for "every host in this subtree", `environments.vgo` for "prod vs. staging", or the match block for one host.

**Naming.** A foreach instance and its resources are suffixed with `[<key>]` — module `vhost[www.example.com]`, resource `vhost-conf[www.example.com]` — so instances never collide. You write plain resource names in the module; the loader does the suffixing, and rewrites intra-module `depends_on`/`notify`/`subscribes`/`before` references to match (so `depends_on: [vhost-docroot]` becomes `depends_on: [vhost-docroot[www.example.com]]` inside that instance, while a reference to a resource in another module — `notify: [nginx-reload]` — is left alone).

**Keys.**

- A list of **scalars** (`foreach: cache_kinds` over `[nginx, redis]`) is keyed by the value: instances `cache[nginx]`, `cache[redis]`. Omit `key:`.
- A list of **maps** requires `key:` on the ref naming which field supplies the suffix. It's a load-time error to omit it, to point `key:` at a field an item lacks, or for two items to resolve to the same key.

**Var defaults.** A foreach instance still gets the module's `vars:` defaults (`doc_root_base` above), merged with the envoy's vars as usual. The `{{ .Each.<field> }}` values come from the list item and are substituted at config-load time; `{{ .Vars.<field> }}` and `{{ .Traits.<field> }}` resolve at check-in like any other module. Because `.Each` is substituted by name at load time, **every item must provide every `.Each` field the module references** — a leftover `{{ .Each.something }}` is a load error, not a silent skip.

**Per-instance variation** is interpolation (`{{ .Each.port }}`) plus, for "this instance also gets an extra resource", a resource in the module gated on `when:` against an item field:

```yaml
  # in modules/vhost.vgo
  - name: vhost-extra-locations
    type: blockinfile
    when: "'{{ .Each.has_extra }}' == 'yes'"        # items set has_extra: yes / no
    target_path: "/etc/nginx/sites-enabled/{{ .Each.server_name }}.conf"
    content: "{{ .Each.extra_locations }}"
```

Items then carry `has_extra: yes` (and `extra_locations:`) or `has_extra: no`. Vigo's template language is interpolation-only — there is no `{{ if }}`/`{{ range }}` *inside* a `content:` body — so per-instance structural differences are expressed as separate `when:`-gated resources (or, for a clean split, a second module `foreach:`-ed over a filtered sub-list). This is by design: config stays data, readable at a glance, and statically lintable.

**Limitations.** A module can be `foreach:`-ed under one name at most (the same name used both plainly and with `foreach:`, or two `foreach:` refs of the same module, is collapsed by the first-ref-wins rule — put everything in one list). A resource *inside* a foreach'd module that carries its own resource-level `foreach:` iterates a global var, not the module item — nested data-dependent iteration isn't supported.

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
    target_path: "/etc/nginx/sites-enabled/{{ .Item.name }}.conf"
    content: |
      server {
          server_name {{ .Item.domain }};
          root {{ .Item.root }};
      }
    notify: [reload-nginx]

  - name: "webroot-{{ .Item.name }}"
    type: directory
    foreach: vhosts
    target_path: "{{ .Item.root }}"
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
        target_path: /etc/apt/apt.conf.d/99-vigo
        content: |
          APT::Install-Recommends "false";
      redhat:
        target_path: /etc/yum.conf.d/vigo.conf
        content: |
          installonly_limit=3
```

One resource definition, two behaviors. The `case` expression selects which `match` branch applies.

**Combined with traits for hardware-adaptive config:**

```yaml
resources:
  - name: jvm-heap
    type: file
    target_path: /etc/app/jvm.opts
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
          unless: "test -f /var/lib/apt/lists/lock"
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

### environments.vgo — Per-environment variable overrides

**Problem:** Production, staging, and dev need different values for the same variables.

Declare `environment:` on each match block, then put env-specific vars in `environments.vgo` at the appropriate directory level:

```yaml
# envoys.vgo
envoys:
  - match: "app*.prod.example.com"
    environment: production
    roles: [app-server]
  - match: "app*.staging.example.com"
    environment: staging
    roles: [app-server]
  - match: "app*.dev.example.com"
    environment: development
    roles: [app-server]
```

```yaml
# environments.vgo (alongside envoys.vgo, or at any ancestor directory)
env:
  production:
    vars:
      replicas: 3
      log_level: warn
      debug: false
  staging:
    vars:
      replicas: 1
      log_level: debug
      debug: true
  development:
    vars:
      replicas: 1
      log_level: trace
      debug: true
```

Env is cross-cutting — declaring it on the match block + centralizing the overrides in a sibling file means the same role can serve every tier with no inline per-env boilerplate. See [Multi-Axis Config](multi-axis-config.md).

### fleet: — Pull a value from the rest of the fleet

**Problem:** A load-balancer's backend pool *is* the current set of `web` hosts. A monitoring file should list every host running `node-exporter`. A cluster member list (`etcd --initial-cluster=…`) should track the actual cluster. Hand-maintaining these as a literal list goes stale the moment a host is added or replaced.

**Solution:** a top-level var whose value is a `fleet:` construct resolves, at check-in, to a value derived from the live fleet — every envoy matching a selector, with a chosen field rendered and (optionally) joined into a block:

```yaml
# in a common.vgo / environments.vgo / a match block — wherever vars are set
vars:
  app_backends:
    fleet:
      where: "tag('app') && os_family('linux')"      # selector over each envoy's traits/tags
      select: "    server {{ .IP }}:8080;"            # rendered once per matched envoy
      join: "\n"                                      # glue them into one block (omit → a list)
      include_stale: false                            # default: drop offline envoys
```

```yaml
# the module that consumes it
resources:
  - name: app-upstream
    type: file
    target_path: /etc/nginx/conf.d/app-upstream.conf
    content: |
      upstream app {
      {{ .Vars.app_backends }}
      }
    notify: [reload-nginx]
```

On every check-in the server resolves `app_backends` from its in-memory fleet index — for `web*` and any other host tagged `app` — and the file is rendered with the current pool, sorted (so there's no needless churn from map order). Offline hosts (per the staleness threshold) are excluded unless `include_stale: true`. The result is cached for a short TTL (`checkin.fleet_lookup_ttl`, default 30s) so a check-in is a cache hit, not a fleet walk.

- **`where:`** is the `when:` language plus three fleet-specific builtins: `tag('<glob>')`, `host('<glob>')`, and `trait('<key>', '<value>')` (use `trait()` for dotted trait keys like `os.distro` — the `when:` lexer won't take a dot in a bare identifier). The OS/arch builtins (`os_family`, `distro`, `arch`, `version_ge`, `has_display`) work too. Agent-only builtins (`file_exists`, `has_command`, …) are rejected at config publish — a fleet selector must be fully server-evaluable.
- **`select:`** is a Go template over the matched envoy's `{{ .Hostname }}`, `{{ .IP }}`, `{{ .Tags }}`, and `{{ .Traits }}` (a flat map — `{{ index .Traits "network.primary_ip" }}` for a dotted key). It does **not** see `.Vars` — the value is the same for every host that uses it, derived from the fleet, not from the asking host.
- **`join:`** glues the per-envoy strings into one string for `content:`. Omit it and the var is a list instead — which you can `foreach:` over to produce N resources (N firewall rules, N `sshkey` entries): `foreach:` over a `fleet:` var works (it's expanded after the lookup resolves). `foreach:` needs a list, so don't set `join:` in that case.
- **Where it can appear:** a *top-level* var value only — `myvar: { fleet: {...} }`. Not nested inside another var, and not a resource attribute. Put a `fleet:` in its own var and reference the var.
- **No inline `{{ if }}`/`{{ range }}`** — Vigo's templates are interpolation-only. Per-instance shaping is the `select:` template plus, if you need it, a second `fleet:` var with a narrower `where:`.

`fleet:` is the one cross-node mechanism — every other layer above governs *which static config applies to which host*; `fleet:` is *config derived from the other hosts' live state*. It's eventually-consistent: at an empty fleet the value is empty and fills in as hosts enroll.

## Layer 5: Templates — Dynamic Values from Traits

**Problem:** Config file content needs to reference the machine's actual hostname, IP, CPU count, etc.

```yaml
resources:
  - name: prometheus-config
    type: file
    target_path: /etc/prometheus/prometheus.yml
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

Templates resolve at check-in time using the envoy's actual traits and resolved vars. They're only allowed in `content:` attributes and `source:` template files — not in `command:`, `target_path:`, or other parameters.

## Putting It All Together

A complete example managing a web fleet across two environments:

```yaml
# roles/base.vgo
name: base
modules: [ntp, sshd-config, node-exporter]

# roles/webserver.vgo
name: webserver
includes: [base]
modules:
  - nginx
  - logrotate
  - name: crowdsec
    when: "os_family('debian')"

# modules/nginx.vgo
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
    target_path: "/etc/nginx/sites-enabled/{{ .Item.name }}.conf"
    source: templates/nginx-vhost.tmpl
    notify: [reload-nginx]
  - name: reload-nginx
    type: exec
    command: "nginx -s reload"
    when: "changed"
  - name: nginx-service
    type: service
    service: nginx
    state: running
    enabled: true

# envoys/web-fleet.vgo
envoys:
  - match: "web*.prod.example.com"
    environment: production
    roles: [webserver]
  - match: "web*.staging.example.com"
    environment: staging
    roles: [webserver]

# environments.vgo
env:
  production:
    vars:
      nginx_port: 443
      vhosts:
        - {name: app, domain: app.example.com}
        - {name: api, domain: api.example.com}
  staging:
    vars:
      nginx_port: 8080
      vhosts:
        - {name: app, domain: staging-app.example.com}
```

**What this achieves:**

- `base` role shared across webserver, database, and any future role
- `webserver` role composes base + nginx + logrotate + conditional security
- `nginx` module is fully parameterized — vhosts driven by a var list
- Match blocks stay tiny — one line per tier, env-specific values live in `environments.vgo`
- Per-host snowflakes get their own match block above the glob, or use lookup tables keyed by hostname/tag
- Templates inject actual hostnames, ports, and domains at check-in time

**No duplication.** Adding a new vhost: add one entry to the `vhosts` list. Adding a new environment: add one envoy entry with the environment name. Adding monitoring to all servers: add the module to `base` role. Each change happens in exactly one place.

## Related

- [Config Format](config-format.md) — Module, role, and envoy structure
- [Resource Language](resource-types.md) — foreach, case, conditional_block, defaults, stream_edit
- [When Expressions](when-expressions.md) — Conditional logic syntax
- [Templates](templates.md) — Go template expressions in content
- [Resource DAG](../architecture/resource-dag.md) — Dependency and notification ordering
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
