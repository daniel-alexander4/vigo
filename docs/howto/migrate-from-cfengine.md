
# Migrating from CFEngine

CFEngine and Vigo both model desired state as declarative units that agents evaluate and converge locally. Vigo's resource model maps onto CFEngine promises, configcrates onto bundles, and `when:` expressions onto the class system. The shift is mostly notational (CFEngine policy language → YAML) plus a different identity model on the server side (hostcrates + roles + environments).

## Architecture Differences

Both CFEngine and Vigo use a pull-based agent that fetches policy from a server and converges locally. CFEngine's cf-agent evaluates promises written in the CFEngine DSL; Vigo's agent interprets YAML resources compiled into signed bundles by the server.

CFEngine has multiple cooperating daemons (cf-serverd, cf-execd, cf-monitord, cf-hub). Vigo consolidates into a single server binary (vigosrv) that handles gRPC, REST, metrics, and the web UI.

CFEngine's strength is its lightweight C agent with a 5-minute convergence cycle. Vigo's agent is Rust (single static binary) with a configurable check-in interval and compiled promise bundles that enable offline convergence — the agent caches its last signed bundle and continues applying it when the server is unreachable.

CFEngine's class system is a powerful boolean tagging mechanism for conditional policy. Vigo replaces this with `when:` expressions — a simpler syntax with 15 builtin functions that covers most class-based patterns without the complexity of class arithmetic.

## Concept Mapping

| CFEngine Concept | Vigo Equivalent | Notes |
|---|---|---|
| Promise | Resource | Declarative desired-state unit |
| Bundle | Configcrate (`.vgo` file) | Named collection of resources |
| Body | Defaults / attributes | Resource-level attribute values |
| Namespace | Configcrate name prefix | Flat namespace — prefix to avoid collisions |
| `promises.cf` / `update.cf` | `envoys.vgo` | Policy hub configuration → node classification |
| Hard class (e.g., `debian`) | Trait-based `when:` | `when: "os_family('debian')"` |
| Soft class (defined) | `when:` with `has_trait()` | Boolean conditions on system state |
| Class expression (`debian.webserver`) | `when:` with `&&` | `when: "os_family('debian') && has_trait('role', 'web')"` |
| `cf-serverd` | `vigosrv` | Go binary, gRPC + REST + Web UI |
| `cf-agent` | `vigo` agent | Rust binary, LMDB state store |
| `cf-execd` | Agent systemd service | Agent runs as a daemon with built-in scheduling |
| `cf-monitord` | Traits | Agent-side monitoring replaced by trait collection |
| `cf-hub` | FleetIndex + SQLite | Central data aggregation |
| Masterfiles | `stockpile/` | Policy repository |
| Augments (`def.json`) | `vars` / `common.vgo` / `environments.vgo` | External variable data, layered by inheritance |
| Module protocol | Custom executors | JSON stdin/stdout protocol for extensions |
| `cf-secret` | `secret:` prefix | Secrets resolved at bundle compile time |
| Mustache template | Go template in `content:` | Templates only in `content:` attribute |
| `edit_line` | `file_line` / `blockinfile` / `stream_edit:` | Multiple options for in-place editing |
| `services:` promise type | `service` executor | `state: running/stopped`, `enabled: true/false` |
| `reports:` promise type | No equivalent | Use audit log or task results for reporting |
| `meta:` promise type | No equivalent | No policy metadata system |

## Resource Type Mapping

| CFEngine Promise Type | Vigo Executor | Notes |
|---|---|---|
| `packages:` (policy) | `package` | Auto-detects package manager |
| `files:` (copy) | `file` with `sourcepath:` | Copy from agent-local path |
| `files:` (content) | `file` with `content:` | Inline template |
| `files:` (`edit_line`) | `file_line` / `blockinfile` | Line or block editing |
| `services:` | `service` | `state: running/stopped`, `enabled: true/false` |
| `commands:` | `exec` | `command:`, `onlyif:`, `unless:` |
| `users:` | `user` | `username:`, `uid:`, `shell:`, `groups:` |
| `storage:` (mount) | `mount` | `device:`, `mountpoint:`, `fstype:` |
| `files:` (link) | `symlink` | `target_path:`, `source:` |
| `files:` (perms) | `file` | `owner:`, `group:`, `mode:` |
| `vars:` (sysctl) | `sysctl` | `key:`, `value:` |
| `files:` (`edit_line` insert) | `file_line` | `path:`, `line:`, `match:` |

## Walkthrough: Converting an Nginx Bundle


### CFEngine Version

```cfengine3
bundle agent nginx
{
  vars:
    "worker_processes"   string => "auto";
    "worker_connections" string => "1024";
    "server_name"        string => "localhost";
    "root"               string => "/var/www/html";

  packages:
    "nginx"
      policy => "present",
      package_module => apt;

  files:
    "/etc/nginx/nginx.conf"
      content => mustache("nginx.conf.mustache", "nginx"),
      perms   => mog("0644", "root", "root"),
      classes => if_repaired("nginx_config_changed");

  services:
    "nginx"
      service_policy => "start";

    nginx_config_changed::
    "nginx"
      service_policy => "restart";
}
```

With `nginx.conf.mustache`:

```mustache
worker_processes {{vars.nginx.worker_processes}};
events {
    worker_connections {{vars.nginx.worker_connections}};
}
http {
    include       /etc/nginx/mime.types;
    default_type  application/octet-stream;
    sendfile      on;
    keepalive_timeout 65;

    server {
        listen 80;
        server_name {{vars.nginx.server_name}};
        root {{vars.nginx.root}};

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

- **No class-based restart.** CFEngine uses `classes => if_repaired("nginx_config_changed")` to set a class, then conditionally restarts in a separate `services:` promise guarded by `nginx_config_changed::`. Vigo uses `notify: [nginx-service]` directly — when the file changes, the service restarts.
- **YAML instead of DSL.** CFEngine's promise syntax requires learning its DSL, bodies, and promise types. Vigo is plain YAML with a small set of known keys.
- **Templates inline or in `templates/`.** Small templates go in `content:` using Go template syntax. Larger files use `source: templates/app.conf.tmpl` — the server reads and renders the file from the `templates/` directory.
- **Simpler variable access.** CFEngine's `{{vars.bundle.varname}}` becomes `{{ .Vars.varname }}` in Vigo.

## Variable and Data Hierarchy

CFEngine variables live in bundle scope and can be set via augments (`def.json`). Vigo's hierarchy:

| CFEngine Level | Vigo Equivalent |
|---|---|
| Bundle `vars:` | Configcrate `vars:` (defaults) |
| Augments (`def.json`) | `common.vgo` vars at ancestor directories (inherited) |
| Hard classes | Traits (available in `when:` and templates) |
| `host_specific_data/` | Match-block `vars:` in `envoys.vgo` |
| Environments | `environments.vgo` keyed by `environment:` |

Per-environment overrides live in a sibling file, not inline on each match block:

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

CFEngine uses Mustache templates. Vigo uses Go templates.

| CFEngine Mustache | Vigo Go Template |
|---|---|
| `{{vars.bundle.variable}}` | `{{ .Vars.variable }}` |
| `{{classes.classname}}` | `{{ if .Traits.os.family }}` or `when:` |
| `{{#classes.debian}}...{{/classes.debian}}` | `{{ if eq .Traits.os.family "debian" }}...{{ end }}` |
| `{{%vars.bundle.variable}}` (iterate) | `{{ range .Vars.list }}` |

**Critical restriction:** Templates work only in `content:` attributes. All other resource attributes are literal YAML values.

## Conditionals and Targeting


CFEngine's class system:

```cfengine3
debian::
  packages:
    "nginx"
      policy => "present";

redhat::
  packages:
    "nginx"
      policy => "present";
```

Vigo equivalent using `when:`:

```yaml
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

CFEngine class expressions:

| CFEngine Class Expression | Vigo `when:` |
|---|---|
| `debian` | `os_family('debian')` |
| `debian.webserver` | `os_family('debian') && has_trait('role', 'web')` |
| `!windows` | `!os_family('windows')` |
| `debian\|ubuntu` | `os_family('debian')` (covers both) |
| `any` | (omit `when:` — always applies) |

## Secrets

CFEngine uses `cf-secret` for encrypting data:

```bash
cf-secret encrypt -o secret.dat -k /path/to/pubkey plaintext.txt
```

Vigo uses the `secret:` prefix inline:

```yaml
content: "secret:vigo/database/password"
```

The secrets provider resolves references at bundle compile time. No key management on agents.

## Common Gotchas

1. **No class system.** CFEngine's classes are a global boolean namespace evaluated at every promise. Vigo's `when:` expressions are per-resource and don't propagate. There's no equivalent of setting a class in one resource and reading it in another — use `notify`/`subscribes` for inter-resource triggers.


2. **No convergence locks.** CFEngine's `action => if_elapsed("60")` throttles promise evaluation. Vigo evaluates all resources every check-in. Use the check-in interval to control convergence frequency.

3. **No module protocol output parsing.** CFEngine modules print `+classname` and `=variable=value` to stdout for integration. Vigo's custom executors use a JSON stdin/stdout protocol — different format, same purpose.

4. **Templates only in `content:`.** CFEngine allows Mustache in any file promise. Vigo restricts Go templates to `content:`. Use `when:` for conditional logic in resource attributes.

5. **Flat variable namespace.** CFEngine scopes variables to bundles. Vigo vars are flat strings — prefix with the configcrate name to avoid collisions.

6. **No `meta:` or `reports:`.** CFEngine's metadata and reporting promise types don't exist. Use the audit log, webhooks, or the REST API for observability.

7. **First-match-wins classification.** CFEngine's class-based policy can apply multiple bundles based on multiple matching classes. Vigo's `envoys.vgo` uses first-match-wins — only the first matching glob applies. Order entries from most specific to least.

8. **No `edit_line` convergent editing.** CFEngine's `edit_line` body with convergent insert/delete is powerful. Vigo offers `file_line` (single lines), `blockinfile` (blocks), and `stream_edit:` (pipe through external scripts). For complex file transformations, `stream_edit:` is the closest equivalent.

## Related

- [Config Format](../reference/writing-configcrates.md) — Configcrates, roles, envoys.vgo, vars
- [Resource DAG](../reference/writing-configcrates.md) — depends_on, notify, subscribes
- [Templates](../reference/writing-configcrates.md) — Go template syntax and restrictions
- [When Expressions](../reference/writing-configcrates.md) — Conditional expressions and builtins
- [Secrets](../reference/server-yaml.md) — Secret providers and the secret: prefix
- [Custom Executors](../reference/writing-configcrates.md) — JSON protocol for extensions
- [Executors](../reference/vigo/resources/_index.md) — All resource executors
- [Nginx Configcrate Example](../examples/nginx-module.md) — Full nginx walkthrough
- [First Configcrate](../howto/write-your-first-module.md) — Writing your first configcrate

---
*Verified on Vigo 0.51.6 · 2026-05-13.*

*Confidential — Alexander4, LLC. Not for redistribution. See [../legal/license.md](../legal/license.md).*
