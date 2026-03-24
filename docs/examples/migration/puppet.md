
# Migrating from Puppet

Vigo is the most conceptually similar tool to Puppet — both are pull-based, compile a desired-state catalog on the server, and apply idempotent resources on the agent. If you think in Puppet, Vigo will feel familiar.

## Architecture Differences

Both Puppet and Vigo follow a pull model: an agent contacts a central server, receives desired state, and converges locally. The server compiles a catalog (Puppet) or bundle (Vigo) based on the node's identity and facts/traits.

Puppet uses its own DSL for manifests and ERB/EPP for templates. Vigo uses plain YAML for everything and Go templates for `content:` attributes only. There is no compilation step — the YAML is the config.

Puppet stores catalog state in PuppetDB and queries it for exported resources and reporting. Vigo keeps an in-memory FleetIndex (rehydrated from SQLite on startup) and never touches the database on the check-in hot path.

Puppet's agent runs on a 30-minute interval by default. Vigo's agent interval is configurable and supports compiled promises with offline convergence — the agent caches its last bundle and continues applying it when the server is unreachable.

## Concept Mapping

| Puppet Concept | Vigo Equivalent | Notes |
|---|---|---|
| Manifest (`.pp` file) | Module (`.vgo` file) | YAML instead of Puppet DSL |
| Class / Defined Type | Module | One module = one `.vgo` file with resources |
| Profile | Module | Same level of abstraction |
| Role | Role | Named list of modules |
| `site.pp` / ENC | `nodes.vgo` | First-match-wins glob patterns |
| Hiera | `vars` / `environment_overrides` | 3 levels: module defaults → role vars → node vars |
| Facter fact | Trait | Collected automatically, available in `when:` and templates |
| Catalog | Bundle | Server-compiled desired state sent to agent |
| Resource | Resource | Same concept — idempotent, declarative |
| `require` / `before` | `depends_on` / `before` | DAG ordering |
| `notify` | `notify` | Trigger downstream resource re-application |
| `subscribe` | `subscribes` | Pull-based trigger |
| `ensure => absent` | `state: absent` | Same semantics |
| Puppet Forge module | Module `.vgo` file | No package manager — just YAML files |
| PuppetDB | FleetIndex + SQLite | In-memory index, no external database |
| hiera-eyaml | `secret:` prefix | Resolved at bundle compile time |
| ERB template | Go template in `content:` | Templates only in `content:` — never in `target_path:`, `package:`, etc. |
| `environments/` | `environment_overrides` in `nodes.vgo` | Environment-specific overrides, not directory-based |
| Puppet agent | vigo agent | Rust binary, LMDB state store |
| Puppet server | vigosrv | Go binary, gRPC + REST |
| `puppet cert` | `vigogo tokens` | One-time token enrollment instead of certificate signing |

## Resource Type Mapping

| Puppet Resource | Vigo Executor | Notes |
|---|---|---|
| `package` | `package` | Auto-detects package manager |
| `file` | `file` | Go templates in `content:`, or `sourcepath:` for copies |
| `service` | `service` | `state: running/stopped`, `enabled: true/false` |
| `exec` | `exec` | `command:`, `creates:`, `onlyif:`, `unless:` |
| `user` | `user` | `username:`, `uid:`, `shell:`, `groups:` |
| `group` | `group` | `groupname:`, `gid:`, `members:` |
| `cron` | `cron` | `command:`, `minute:`, `hour:`, etc. |
| `file_line` | `file_line` | `path:`, `line:`, `match:` |
| `yumrepo` / `apt::source` | `repository` | Unified across distros |
| `archive` | `archive` | `url:`, `target_path:`, `checksum:` |
| `mount` | `mount` | `device:`, `mountpoint:`, `fstype:` |
| `sysctl` | `sysctl` | `key:`, `value:` |
| `host` | `host` | `hostname:`, `ip:` |

## Walkthrough: Converting an Nginx Module

### Puppet Version

```puppet
class nginx (
  String $worker_processes   = 'auto',
  Integer $worker_connections = 1024,
  String $server_name        = 'localhost',
  String $root               = '/var/www/html',
) {
  package { 'nginx':
    ensure => installed,
  }

  file { '/etc/nginx/nginx.conf':
    ensure  => file,
    owner   => 'root',
    group   => 'root',
    mode    => '0644',
    content => template('nginx/nginx.conf.erb'),
    notify  => Service['nginx'],
    require => Package['nginx'],
  }

  service { 'nginx':
    ensure => running,
    enable => true,
  }
}
```

With `templates/nginx.conf.erb`:

```erb
worker_processes <%= @worker_processes %>;
events {
    worker_connections <%= @worker_connections %>;
}
http {
    include       /etc/nginx/mime.types;
    default_type  application/octet-stream;
    sendfile      on;
    keepalive_timeout 65;

    server {
        listen 80;
        server_name <%= @server_name %>;
        root <%= @root %>;

        location / {
            try_files $uri $uri/ =404;
        }
    }
}
```

### Vigo Version

`stack/stage/modules/nginx.vgo`:

```yaml
name: nginx
vars:
  nginx_worker_processes: auto
  nginx_worker_connections: 1024
  nginx_server_name: localhost
  nginx_root: /var/www/html
resources:
  - name: nginx-package
    type: package
    package: nginx
    state: present

  - name: nginx-config
    type: file
    target_path: /etc/nginx/nginx.conf
    owner: root
    group: root
    mode: "0644"
    content: |
      worker_processes {{ .Vars.nginx_worker_processes }};
      events {
          worker_connections {{ .Vars.nginx_worker_connections }};
      }
      http {
          include       /etc/nginx/mime.types;
          default_type  application/octet-stream;
          sendfile      on;
          keepalive_timeout 65;

          server {
              listen 80;
              server_name {{ .Vars.nginx_server_name }};
              root {{ .Vars.nginx_root }};

              location / {
                  try_files $uri $uri/ =404;
              }
          }
      }
    notify:
      - nginx-service

  - name: nginx-service
    type: service
    service: nginx
    state: running
    enabled: true
```

`stack/stage/roles/webserver.vgo`:

```yaml
name: webserver
modules:
  - nginx
```

`stack/stage/envoys/nodes.vgo`:

```yaml
envoys:
  - match: "*.web.example.com"
    environment: production
    roles: [webserver]
    vars:
      nginx_worker_processes: "4"
      nginx_worker_connections: "4096"
      nginx_server_name: "example.com"
      nginx_root: /var/www/example.com
```

### Key Differences

- **Templates inline or in `templates/`.** Small templates go inline in `content:`. Larger files use `source: templates/nginx.conf.tmpl` — the server reads and renders the file from the `templates/` directory. No ERB — Go template syntax only.
- **No `require =>`** needed for the package → config ordering. Vigo uses `notify:` from config to service, and the resource DAG handles the rest. You can add explicit `depends_on: [nginx-package]` if you prefer.
- **Variables are flat.** Puppet class parameters become `vars:` entries. No scope hierarchy — module defaults, then node-level overrides. That's it.
- **No catalog compilation.** The YAML is interpreted directly. No parser, no AST, no compilation errors to debug.

## Variable and Data Hierarchy

Puppet's Hiera provides up to 10+ hierarchy levels (common.yaml → OS family → environment → datacenter → FQDN). Vigo deliberately has three:

1. **Module defaults** — `vars:` in the module `.vgo` file
2. **Role vars** — `vars:` in the role `.vgo` file (if needed)
3. **Node vars** — `vars:` in the `nodes.vgo` entry

Node vars override role vars, which override module defaults. That's the entire hierarchy. For environment-specific values, use `environment_overrides:`:

```yaml
envoys:
  - match: "*.web.example.com"
    roles: [webserver]
    vars:
      nginx_server_name: "example.com"
    environment_overrides:
      staging:
        nginx_server_name: "staging.example.com"
```

For external data, `vars_from:` loads values from a YAML file at check-in time.

## Templates

Puppet uses ERB (or EPP). Vigo uses Go templates. Key syntax differences:

| Puppet ERB | Vigo Go Template |
|---|---|
| `<%= @variable %>` | `{{ .Vars.variable }}` |
| `<%= scope['module::param'] %>` | `{{ .Vars.param }}` |
| `<%= @fqdn %>` | `{{ .Traits.identity.fqdn }}` |
| `<% if @condition %>` | `{{ if .Vars.condition }}` |
| `<% @items.each do |item| %>` | `{{ range .Vars.items }}` |
| `<%= @variable.downcase %>` | `{{ lower .Vars.variable }}` |

**Critical restriction:** Templates are only allowed in `content:` attributes. You cannot template `target_path:`, `package:`, `service:`, or any other attribute. Use `when:` expressions for conditional logic and separate resources for OS-specific packages.

## Conditionals and Targeting

Puppet conditionals:

```puppet
if $facts['os']['family'] == 'Debian' {
  package { 'nginx': ensure => installed }
} elsif $facts['os']['family'] == 'RedHat' {
  package { 'nginx': ensure => installed, name => 'nginx' }
}
```

Vigo equivalent using `when:`:

```yaml
resources:
  - name: nginx-package-debian
    type: package
    package: nginx
    state: present
    when: "os_family('debian')"

  - name: nginx-package-redhat
    type: package
    package: nginx
    state: present
    when: "os_family('redhat')"
```

Vigo's `when:` expressions support 15 builtin functions (`os_family()`, `is_container`, `has_trait()`, `trait_value()`, etc.) with boolean operators (`&&`, `||`, `!`).

## Secrets

Puppet uses hiera-eyaml to encrypt values in Hiera YAML files. Vigo uses the `secret:` prefix:

```yaml
# Puppet (hiera-eyaml)
nginx::ssl_key: ENC[PKCS7,MIIBe...]

# Vigo
content: "secret:vigo/nginx/ssl_key"
```

The secrets provider resolves `secret:` references at bundle compile time. The default `local` backend maps paths to AES-256-GCM encrypted files. Secrets never appear in plaintext in configs, logs, or gRPC payloads.

## Common Gotchas

1. **No auto-require.** Puppet automatically orders `Package` before `File` before `Service`. Vigo requires explicit `depends_on:` or `notify:`/`subscribes:` for ordering. Without them, resources may execute in any order.

2. **Templates only in `content:`.** Puppet lets you template any attribute value. Vigo restricts Go templates to the `content:` attribute. For OS-conditional packages, use `when:` with separate resources — don't try to template the `package:` field.

3. **No exported resources.** Puppet's `@@` exported resources and PuppetDB collectors don't have a direct equivalent. Use `vars_from:` to share data between nodes, or coordinate through task/workflow orchestration.

4. **Flat variable namespace.** There's no `$module::param` scoping. All vars are flat strings in a single namespace. Prefix var names with the module name (e.g., `nginx_worker_processes`) to avoid collisions.

5. **No `ensure => absent` for everything.** Not all executors support `state: absent`. Check the executor documentation for available states.

6. **First-match-wins.** Puppet's node classification can merge multiple matching entries. Vigo's `nodes.vgo` uses first-match-wins — only the first matching glob applies. Order your entries from most specific to least specific.

7. **No Puppet Forge.** There's no package manager for modules. Modules are plain `.vgo` YAML files in your config directory. Copy, adapt, and version-control them directly.

8. **Exec refresh.** Puppet's `exec` resources use `refreshonly => true` to only run on notification. Vigo uses `when: "changed"` — the resource is skipped on normal check-ins and only runs when triggered by `notify`, `subscribes`, or `watch_secret`.

## Related

- [Config Format](../../concepts/config-format.md) — Modules, roles, nodes.vgo, vars
- [Resource DAG](../../concepts/resource-dag.md) — depends_on, notify, subscribes
- [Templates](../../concepts/templates.md) — Go template syntax and restrictions
- [When Expressions](../../concepts/when-expressions.md) — Conditional expressions and builtins
- [Secrets](../../concepts/secrets.md) — Secret providers and the secret: prefix
- [Executors](../../reference/executors/_index.md) — All resource executors
- [Nginx Module Example](../../examples/nginx-module.md) — Full nginx walkthrough
- [First Module](../../getting-started/first-module.md) — Writing your first module
