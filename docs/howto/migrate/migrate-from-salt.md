
# Migrating from Salt

Salt and Vigo both use YAML for state definitions, both have system facts (Salt grains, Vigo traits), and both support a pull-based agent model. The biggest practical shifts: template rendering (Jinja2 → Go templates scoped to `content:`), data lookup (Salt pillar → Vigo `secret:` resolution + hostcrate vars + lookups), and execution model (Salt's master-issued commands → Vigo's pulled-policy convergence).

## Architecture Differences

Salt supports both push (default via ZeroMQ) and pull (`salt-call --local` or salt-minion with `masterless` mode). Vigo is pull-only — agents poll the server on a configurable interval over gRPC/mTLS.

Salt renders state files through Jinja2 before parsing them as YAML, meaning any YAML value can contain Jinja2 logic. Vigo keeps YAML strictly literal — Go templates are only allowed inside `content:` attributes. This is a fundamental design difference that affects how you structure configs.

Salt's master stores pillar data, orchestrates via the event bus, and can push to minions in real-time. Vigo's server compiles bundles on demand during each check-in. For real-time operations, Vigo uses the task and query dispatch system through agent streams.

Salt requires Python on both master and minion. Vigo's server is Go (vigosrv) and the agent is a single static Rust binary — no runtime dependencies on managed nodes.

## Concept Mapping

| Salt Concept | Vigo Equivalent | Notes |
|---|---|---|
| State file (`.sls`) | Configcrate (`.vgo` file) | Both YAML, different rendering rules |
| `top.sls` | `envoys.vgo` | Glob matching → role/configcrate assignment |
| Formula | Configcrate | Reusable state set — same concept |
| Pillar | `vars` / `common.vgo` / `environments.vgo` | Server-side data for nodes, layered via configcrate → inheritance → match → env |
| Grains | Traits | System facts discovered by the agent |
| Mine | No equivalent | No agent-to-agent data sharing |
| Requisite (`require`) | `depends_on` | DAG ordering |
| Requisite (`watch`) | `subscribes` | Re-apply when dependency changes |
| Requisite (`require_in`) | `before` | Reverse dependency declaration |
| Requisite (`listen`) | `notify` | Trigger downstream resources |
| Jinja2 in states | Go template in `content:` only | Vigo does not render templates in YAML structure |
| `salt-call --local` | Offline convergence | Agent uses cached bundle when server is unreachable |
| `salt-run` | `vigocli task run` | Orchestration on the server side |
| Salt event bus | Agent stream | Persistent gRPC stream for real-time dispatch |
| `salt-cloud` | No equivalent | Vigo does not provision infrastructure |
| Pillar GPG | `secret:` prefix | Secrets resolved server-side |
| Beacons | No equivalent | Agent does not emit events |
| Reactor | Webhooks | Event-driven automation via webhook dispatch |
| Environments | `environments.vgo` (keyed by match-block `environment:`) | Env-specific overrides live in a sibling file |
| `salt` CLI | `vigocli` | Server management CLI |
| `salt-minion` | `vigo` agent | Rust binary, LMDB state store |
| `salt-master` | `vigosrv` | Go binary, gRPC + REST + Web UI |

## Resource Type Mapping

| Salt State Module | Vigo Executor | Notes |
|---|---|---|
| `pkg.installed` | `package` | Auto-detects package manager |
| `file.managed` | `file` | `content:` or `sourcepath:` |
| `service.running` | `service` | `state: running`, `enabled: true` |
| `cmd.run` | `exec` | `command:`, `onlyif:`, `unless:` |
| `user.present` | `user` | `username:`, `uid:`, `shell:`, `groups:` |
| `group.present` | `group` | `groupname:`, `gid:`, `members:` |
| `cron.present` | `cron` | `command:`, `minute:`, `hour:`, etc. |
| `pkgrepo.managed` | `repository` | Unified across distros |
| `archive.extracted` | `source_package` | `url:`, `target_path:`, `checksum:` (tarballs/zips) |
| `pkg.installed` (with `sources:` URL) | `nonrepo_package` | Install .deb/.rpm/.pkg/.msi from URLs |
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

### Vigo Version

`stockpile/configcrates/nginx.vgo`:

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


`stockpile/roles/webserver.vgo`:

```yaml
name: webserver
configcrates:
  - nginx
```

`stockpile/envoys/envoys.vgo`:

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

- **No Jinja2 in YAML structure.** Salt's biggest power and biggest footgun is Jinja2 rendering the YAML itself. Vigo keeps YAML structure static — templates only work inside `content:` values.
- **No `salt://` file server.** Salt serves templates and files via `salt://` URIs. Vigo uses `content:` for inline templates or `source: templates/app.conf.tmpl` for external template files — no file server needed.
- **Requisites map directly.** Salt's `require`/`watch`/`listen`/`require_in` map cleanly to Vigo's `depends_on`/`subscribes`/`notify`/`before`. The semantics are nearly identical.
- **No pillar.get() lookups.** Variables are accessed directly via `{{ .Vars.name }}` in templates or defined as configcrate defaults — no lookup function needed.

## Variable and Data Hierarchy

Salt's pillar data is hierarchical and can be merged across multiple sources. Vigo simplifies this:

| Salt Level | Vigo Equivalent |
|---|---|
| Pillar (formula defaults) | Configcrate `vars:` |
| Pillar (shared across nodes) | `common.vgo` at ancestor directories (inherited down the tree) |
| Pillar (per-environment) | `environments.vgo` keyed by match-block `environment:` |
| Pillar (per-minion) | Match-block `vars:` in `envoys.vgo` |
| Grains | Traits (`.Traits` in templates, builtins in `when:`) |
| Mine | No equivalent — use `common.vgo` for shared data |

Per-environment overrides live in a sibling file, not per-match-block:

```yaml
# envoys.vgo
envoys:
  - match: "prod-*.web.example.com"
    environment: production
    roles: [webserver]
  - match: "stg-*.web.example.com"
    environment: staging
    roles: [webserver]

# environments.vgo
env:
  production:
    vars:
      nginx_server_name: "example.com"
  staging:
    vars:
      nginx_server_name: "staging.example.com"
```

## Templates

Salt uses Jinja2 in both state files and template files. Vigo uses Go templates only in `content:`.

| Salt Jinja2 | Vigo Go Template |

|---|---|
| `{{ variable }}` | `{{ .Vars.variable }}` |
| `{{ grains['os_family'] }}` | `{{ .Traits.os.family }}` |
| `{{ grains['fqdn'] }}` | `{{ .Traits.identity.fqdn }}` |
| `{% if condition %}` | `{{ if .Vars.condition }}` |
| `{% for item in list %}` | `{{ range .Vars.list }}` |
| `{{ salt['pillar.get']('key', 'default') }}` | `{{ default .Vars.key "default" }}` |
| `{{ variable \| lower }}` | `{{ lower .Vars.variable }}` |

**Critical restriction:** You cannot Jinja2-render the YAML structure in Vigo. `{% if %}` blocks that conditionally include resources must become separate resources with `when:` expressions.

## Conditionals and Targeting

Salt conditionals using Jinja2 in state files:

```yaml
{% if grains['os_family'] == 'Debian' %}
nginx-package:
  pkg.installed:
    - name: nginx
{% endif %}
```

Vigo equivalent using `when:`:

```yaml
- name: nginx-package
  type: package
  package: nginx
  state: present
  when: "os_family('debian')"
```

Salt can also use `onlyif`/`unless` on state calls. Vigo's `when:` covers both structural conditionals and runtime guards.

## Secrets

Salt uses GPG-encrypted pillar data or external pillar modules:

```yaml
# pillar/secrets.sls (GPG encrypted)
db_password: |
  -----BEGIN PGP MESSAGE-----
  ...
  -----END PGP MESSAGE-----
```

Vigo uses the `secret:` prefix:

```yaml
content: "secret:vigo/database/password"
```

Secrets are resolved server-side at bundle compile time. No GPG key management on minions.

## Common Gotchas

1. **No Jinja2 in YAML structure.** This is the #1 adjustment. Salt states commonly use `{% if %}` to conditionally include entire resource blocks. In Vigo, every resource is always present in the YAML — use `when:` to conditionally skip execution.

2. **No execution modules.** Salt's `salt['cmd.run']()` or `salt['file.read']()` in Jinja2 has no equivalent. Vigo does not execute arbitrary code during config compilation. Use traits for system facts, directory-level `common.vgo` for shared data, or pre-populate vars.


3. **No pillar merging.** Salt can merge pillar data from multiple sources and environments. Vigo uses a simple three-level override: configcrate defaults → role vars → node vars. Last writer wins at each level.

4. **No `salt://` file URIs.** Salt serves files from the file_roots via `salt://`. Vigo inlines templates in `content:` or uses `sourcepath:` for agent-local files. There is no file server.

5. **Templates only in `content:`.** Salt renders Jinja2 in any YAML value. Vigo restricts Go templates to `content:`. Do not attempt to template `package:`, `target_path:`, or `command:`.

6. **No compound targeting.** Salt's compound matchers (`G@os:CentOS and P@hostname:web.*`) don't have a direct equivalent. Use glob patterns in `envoys.vgo` and `when:` on individual resources for fine-grained targeting.

7. **No event bus or reactor.** Salt's event-driven reactor system doesn't exist in Vigo. For event-driven workflows, use webhooks or the task system.

8. **Agent enrollment required.** Salt minions auto-register via key acceptance. Vigo requires explicit enrollment via a one-time token generated with `vigocli tokens generate`.

## Related

- [Config Format](../../reference/writing-configcrates.md) — Configcrates, roles, envoys.vgo, vars
- [Resource DAG](../../reference/writing-configcrates.md) — depends_on, notify, subscribes
- [Templates](../../reference/writing-configcrates.md) — Go template syntax and restrictions
- [When Expressions](../../reference/writing-configcrates.md) — Conditional expressions and builtins
- [Secrets](../../reference/server-yaml.md) — Secret providers and the secret: prefix
- [Executors](../../reference/vigo/resources/_index.md) — All resource executors
- [Nginx Configcrate Example](../../examples/nginx-module.md) — Full nginx walkthrough
- [First Configcrate](../../howto/configcrates/write-your-first-module.md) — Writing your first configcrate

---
*Verified on Vigo 0.51.6 · 2026-05-13.*

*Confidential — Alexander4, LLC. Not for redistribution. See [../legal/license.md](../../legal/license.md).*
