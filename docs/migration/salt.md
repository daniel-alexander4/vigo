# Migrating from Salt

Hobnob and Salt share a YAML-first configuration philosophy, making Salt the smoothest migration path. Both use YAML for state definitions, have a concept of system facts (grains/traits), and support a pull-based agent model. The key differences are template rendering, data lookup, and execution model.

## Architecture Differences

Salt supports both push (default via ZeroMQ) and pull (`salt-call --local` or salt-minion with `masterless` mode). Hobnob is pull-only — agents poll the server on a configurable interval over gRPC/mTLS.

Salt renders state files through Jinja2 before parsing them as YAML, meaning any YAML value can contain Jinja2 logic. Hobnob keeps YAML strictly literal — Go templates are only allowed inside `content:` attributes. This is a fundamental design difference that affects how you structure configs.

Salt's master stores pillar data, orchestrates via the event bus, and can push to minions in real-time. Hobnob's server compiles bundles on demand during each check-in. For real-time operations, Hobnob uses the task and query dispatch system through agent streams.

Salt requires Python on both master and minion. Hobnob's server is Go (hobd) and the agent is a single static Rust binary — no runtime dependencies on managed nodes.

## Concept Mapping

| Salt Concept | Hobnob Equivalent | Notes |
|---|---|---|
| State file (`.sls`) | Module (`.hnb` file) | Both YAML, different rendering rules |
| `top.sls` | `nodes.hnb` | Glob matching → role/module assignment |
| Formula | Module | Reusable state set — same concept |
| Pillar | `vars` / `environment_overrides` | Server-side data for nodes |
| Grains | Traits | System facts discovered by the agent |
| Mine | No equivalent | No agent-to-agent data sharing |
| Requisite (`require`) | `depends_on` | DAG ordering |
| Requisite (`watch`) | `subscribes` | Re-apply when dependency changes |
| Requisite (`require_in`) | `before` | Reverse dependency declaration |
| Requisite (`listen`) | `notify` | Trigger downstream resources |
| Jinja2 in states | Go template in `content:` only | Hobnob does not render templates in YAML structure |
| `salt-call --local` | Offline convergence | Agent uses cached bundle when server is unreachable |
| `salt-run` | `hobnobcli task run` | Orchestration on the server side |
| Salt event bus | Agent stream | Persistent gRPC stream for real-time dispatch |
| `salt-cloud` | No equivalent | Hobnob does not provision infrastructure |
| Pillar GPG | `secret:` prefix | Secrets resolved server-side |
| Beacons | No equivalent | Agent does not emit events |
| Reactor | Webhooks | Event-driven automation via webhook dispatch |
| Environments | `environment_overrides:` in `nodes.hnb` | Environment-specific overrides |
| `salt` CLI | `hobnobcli` | Server management CLI |
| `salt-minion` | `hobnob` agent | Rust binary, LMDB state store |
| `salt-master` | `hobd` | Go binary, gRPC + REST + Web UI |

## Resource Type Mapping

| Salt State Module | Hobnob Executor | Notes |
|---|---|---|
| `pkg.installed` | `package` | Auto-detects package manager |
| `file.managed` | `file` | `content:` or `sourcepath:` |
| `service.running` | `service` | `state: running`, `enabled: true` |
| `cmd.run` | `exec` | `command:`, `creates:`, `onlyif:`, `unless:` |
| `user.present` | `user` | `username:`, `uid:`, `shell:`, `groups:` |
| `group.present` | `group` | `groupname:`, `gid:`, `members:` |
| `cron.present` | `cron` | `command:`, `minute:`, `hour:`, etc. |
| `pkgrepo.managed` | `repository` | Unified across distros |
| `archive.extracted` | `archive` | `url:`, `targetpath:`, `checksum:` |
| `mount.mounted` | `mount` | `device:`, `mountpoint:`, `fstype:` |
| `sysctl.present` | `sysctl` | `key:`, `value:` |
| `file.blockreplace` | `blockinfile` | `path:`, `block:`, `marker:` |
| `file.line` | `file_line` | `path:`, `line:`, `match:` |
| `host.present` | `host` | `hostname:`, `ip:` |

## Walkthrough: Converting an Nginx State

### Salt Version

`nginx/init.sls`:

```yaml
{% set nginx = salt['pillar.get']('nginx', {}) %}
{% set worker_processes = nginx.get('worker_processes', 'auto') %}
{% set worker_connections = nginx.get('worker_connections', 1024) %}
{% set server_name = nginx.get('server_name', 'localhost') %}
{% set root = nginx.get('root', '/var/www/html') %}

nginx-package:
  pkg.installed:
    - name: nginx

nginx-config:
  file.managed:
    - name: /etc/nginx/nginx.conf
    - source: salt://nginx/files/nginx.conf.j2
    - template: jinja
    - user: root
    - group: root
    - mode: '0644'
    - context:
        worker_processes: {{ worker_processes }}
        worker_connections: {{ worker_connections }}
        server_name: {{ server_name }}
        root: {{ root }}
    - require:
      - pkg: nginx-package
    - watch_in:
      - service: nginx-service

nginx-service:
  service.running:
    - name: nginx
    - enable: True
```

`nginx/files/nginx.conf.j2`:

```jinja2
worker_processes {{ worker_processes }};
events {
    worker_connections {{ worker_connections }};
}
http {
    include       /etc/nginx/mime.types;
    default_type  application/octet-stream;
    sendfile      on;
    keepalive_timeout 65;

    server {
        listen 80;
        server_name {{ server_name }};
        root {{ root }};

        location / {
            try_files $uri $uri/ =404;
        }
    }
}
```

### Hobnob Version

`thrall-configs/modules/nginx.hnb`:

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
    targetpath: /etc/nginx/nginx.conf
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

`thrall-configs/roles/webserver.hnb`:

```yaml
name: webserver
modules:
  - nginx
```

`thrall-configs/nodes.hnb`:

```yaml
thralls:
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

- **No Jinja2 in YAML structure.** Salt's biggest power and biggest footgun is Jinja2 rendering the YAML itself. Hobnob keeps YAML structure static — templates only work inside `content:` values.
- **No `salt://` file server.** Salt serves templates and files via `salt://` URIs. Hobnob uses `content:` for inline templates or `source: templates/app.conf.tmpl` for external template files — no file server needed.
- **Requisites map directly.** Salt's `require`/`watch`/`listen`/`require_in` map cleanly to Hobnob's `depends_on`/`subscribes`/`notify`/`before`. The semantics are nearly identical.
- **No pillar.get() lookups.** Variables are accessed directly via `{{ .Vars.name }}` in templates or defined as module defaults — no lookup function needed.

## Variable and Data Hierarchy

Salt's pillar data is hierarchical and can be merged across multiple sources. Hobnob simplifies this:

| Salt Level | Hobnob Equivalent |
|---|---|
| Pillar (formula defaults) | Module `vars:` |
| Pillar (per-environment) | `environment_overrides:` in `nodes.hnb` |
| Pillar (per-minion) | Node `vars:` in `nodes.hnb` |
| Grains | Traits (`.Traits` in templates, builtins in `when:`) |
| Mine | No equivalent — use `vars_from:` for shared data |

Environment-specific overrides:

```yaml
thralls:
  - match: "*.web.example.com"
    roles: [webserver]
    vars:
      nginx_server_name: "example.com"
    environment_overrides:
      staging:
        nginx_server_name: "staging.example.com"
```

## Templates

Salt uses Jinja2 in both state files and template files. Hobnob uses Go templates only in `content:`.

| Salt Jinja2 | Hobnob Go Template |
|---|---|
| `{{ variable }}` | `{{ .Vars.variable }}` |
| `{{ grains['os_family'] }}` | `{{ .Traits.os.family }}` |
| `{{ grains['fqdn'] }}` | `{{ .Traits.identity.fqdn }}` |
| `{% if condition %}` | `{{ if .Vars.condition }}` |
| `{% for item in list %}` | `{{ range .Vars.list }}` |
| `{{ salt['pillar.get']('key', 'default') }}` | `{{ default .Vars.key "default" }}` |
| `{{ variable \| lower }}` | `{{ lower .Vars.variable }}` |

**Critical restriction:** You cannot Jinja2-render the YAML structure in Hobnob. `{% if %}` blocks that conditionally include resources must become separate resources with `when:` expressions.

## Conditionals and Targeting

Salt conditionals using Jinja2 in state files:

```yaml
{% if grains['os_family'] == 'Debian' %}
nginx-package:
  pkg.installed:
    - name: nginx
{% endif %}
```

Hobnob equivalent using `when:`:

```yaml
- name: nginx-package
  type: package
  package: nginx
  state: present
  when: "os_family('debian')"
```

Salt can also use `onlyif`/`unless` on state calls. Hobnob's `when:` covers both structural conditionals and runtime guards.

## Secrets

Salt uses GPG-encrypted pillar data or external pillar modules:

```yaml
# pillar/secrets.sls (GPG encrypted)
db_password: |
  -----BEGIN PGP MESSAGE-----
  ...
  -----END PGP MESSAGE-----
```

Hobnob uses the `secret:` prefix:

```yaml
content: "secret:hobnob/database/password"
```

Secrets are resolved server-side at bundle compile time. No GPG key management on minions.

## Common Gotchas

1. **No Jinja2 in YAML structure.** This is the #1 adjustment. Salt states commonly use `{% if %}` to conditionally include entire resource blocks. In Hobnob, every resource is always present in the YAML — use `when:` to conditionally skip execution.

2. **No execution modules.** Salt's `salt['cmd.run']()` or `salt['file.read']()` in Jinja2 has no equivalent. Hobnob does not execute arbitrary code during config compilation. Use traits for system facts, `vars_from:` for data, or pre-populate vars.

3. **No pillar merging.** Salt can merge pillar data from multiple sources and environments. Hobnob uses a simple three-level override: module defaults → role vars → node vars. Last writer wins at each level.

4. **No `salt://` file URIs.** Salt serves files from the file_roots via `salt://`. Hobnob inlines templates in `content:` or uses `sourcepath:` for agent-local files. There is no file server.

5. **Templates only in `content:`.** Salt renders Jinja2 in any YAML value. Hobnob restricts Go templates to `content:`. Do not attempt to template `package:`, `targetpath:`, or `command:`.

6. **No compound targeting.** Salt's compound matchers (`G@os:CentOS and P@hostname:web.*`) don't have a direct equivalent. Use glob patterns in `nodes.hnb` and `when:` on individual resources for fine-grained targeting.

7. **No event bus or reactor.** Salt's event-driven reactor system doesn't exist in Hobnob. For event-driven workflows, use webhooks or the task system.

8. **Agent enrollment required.** Salt minions auto-register via key acceptance. Hobnob requires explicit enrollment via a one-time token generated with `hobnobcli tokens generate`.

## Related

- [Config Format](../concepts/config-format.md) — Modules, roles, nodes.hnb, vars
- [Resource DAG](../concepts/resource-dag.md) — depends_on, notify, subscribes
- [Templates](../concepts/templates.md) — Go template syntax and restrictions
- [When Expressions](../concepts/when-expressions.md) — Conditional expressions and builtins
- [Secrets](../concepts/secrets.md) — Secret providers and the secret: prefix
- [Executors](../executors/_index.md) — All resource executors
- [Nginx Module Example](../examples/nginx-module.md) — Full nginx walkthrough
- [First Module](../getting-started/first-module.md) — Writing your first module
