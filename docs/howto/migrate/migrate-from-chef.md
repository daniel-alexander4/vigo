
# Migrating from Chef

Chef and Vigo share the pull-based model and converge-on-interval pattern. The biggest practical shifts: Ruby DSL → plain YAML; Chef's 15-level attribute precedence → Vigo's three levels (match-block vars > inherited vars > configcrate defaults, with `environments.vgo` overrides at the top). The resource shape — declarative, idempotent, with `notify` / `subscribes` triggers — maps closely.

## Architecture Differences

Both Chef and Vigo use a pull-based agent that contacts a server on a regular interval, receives desired state, and converges locally. Chef compiles Ruby code into a resource collection; Vigo interprets YAML directly — there's no compilation step.

Chef's infrastructure includes a Chef Server (or Chef Automate), client nodes running chef-client (Ruby), and often a Supermarket for cookbook distribution. Vigo has a Go server (vigosrv), a Rust agent (vigo), and a CLI (vigocli). No Ruby runtime, no package manager, no external services.

Chef stores node objects and search indexes on the server (PostgreSQL + Elasticsearch in Chef Server). Vigo uses an in-memory FleetIndex backed by SQLite — no external database.

Chef's attribute system has 15 precedence levels (default, force_default, normal, override, force_override, automatic — each at role, environment, cookbook, and recipe levels). Vigo has three levels: configcrate defaults, role vars, node vars.

## Concept Mapping

| Chef Concept | Vigo Equivalent | Notes |
|---|---|---|
| Cookbook | Configcrate (`.vgo` file) | Single YAML file replaces the cookbook directory tree |
| Recipe | Configcrate resources | Resources listed in the configcrate — no separate recipe files |
| Role | Role | Named list of configcrates — same concept |
| Run list | Configcrate list in `envoys.vgo` | Roles and configcrates assigned to matching envoys |
| Node object | FleetIndex entry + Traits | In-memory, not stored server-side as a document |
| Ohai | Traits | System facts collected by the agent |
| Attributes (default) | Configcrate `vars:` | Default values in the configcrate |
| Attributes (override) | Match-block `vars:` in `envoys.vgo` | Match-level overrides |
| Data bag | `common.vgo` at any directory level | Directory-inherited shared vars replace external YAML data files |
| Chef Vault | `secret:` prefix | Resolved server-side, never in plaintext |
| Environment | `environments.vgo` (keyed by `environment:`) | Env-specific overrides live in a sibling file, not per-match-block |
| `notifies` | `notify` | Trigger downstream resource on change |
| `subscribes` | `subscribes` | Same keyword, same semantics |
| `only_if` / `not_if` | `when:` | Resource conditionals |
| `include_recipe` | Configcrate dependencies | Configcrates listed in roles or `depends_on` |
| `knife` | `vigocli` | CLI for server administration |
| Chef Supermarket | No equivalent | Configcrates are plain YAML files — no registry |
| Policyfile | `envoys.vgo` | Pin configcrate versions at the node level |
| `chef-client` | `vigo` agent | Rust binary, LMDB state, offline convergence |
| `chef-server` | `vigosrv` | Go binary, gRPC + REST + Web UI |
| ERB template | Go template in `content:` | Templates only in `content:` attribute |
| `knife bootstrap` | `vigocli tokens` + bootstrap script | One-time token enrollment |

## Resource Type Mapping

| Chef Resource | Vigo Executor | Notes |
|---|---|---|
| `package` | `package` | Auto-detects package manager |
| `template` / `cookbook_file` | `file` | `content:` for templates, `sourcepath:` for static files |
| `service` | `service` | `state: running/stopped`, `enabled: true/false` |
| `execute` / `bash` | `exec` | `command:`, `onlyif:`, `unless:` |
| `user` | `user` | `username:`, `uid:`, `shell:`, `groups:` |
| `group` | `group` | `groupname:`, `gid:`, `members:` |
| `cron` | `cron` | `command:`, `minute:`, `hour:`, etc. |
| `apt_repository` / `yum_repository` | `repository` | Unified across distros |
| `remote_file` (archive) | `source_package` | `url:`, `target_path:`, `checksum:` (tarballs/zips) |
| `remote_file` + `dpkg_package`/`rpm_package` | `nonrepo_package` | Install .deb/.rpm/.pkg/.msi from URLs |
| `mount` | `mount` | `device:`, `mountpoint:`, `fstype:` |
| `sysctl` | `sysctl` | `key:`, `value:` |
| `hostname` | `hostname` | `hostname:` |
| `file` (line) | `file_line` | `path:`, `line:`, `match:` |


## Walkthrough: Converting an Nginx Cookbook

### Chef Version

`cookbooks/nginx/recipes/default.rb`:

```ruby
package 'nginx' do
  action :install
end

template '/etc/nginx/nginx.conf' do
  source 'nginx.conf.erb'
  owner 'root'
  group 'root'
  mode '0644'
  variables(
    worker_processes: node['nginx']['worker_processes'],
    worker_connections: node['nginx']['worker_connections'],
    server_name: node['nginx']['server_name'],
    root: node['nginx']['root']
  )
  notifies :restart, 'service[nginx]'
end

service 'nginx' do
  action [:enable, :start]
end
```

`cookbooks/nginx/attributes/default.rb`:

```ruby
default['nginx']['worker_processes'] = 'auto'
default['nginx']['worker_connections'] = 1024
default['nginx']['server_name'] = 'localhost'
default['nginx']['root'] = '/var/www/html'
```

`cookbooks/nginx/templates/default/nginx.conf.erb`:

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

`stacks/configcrates/nginx.vgo`:

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

`stacks/roles/webserver.vgo`:


```yaml
name: webserver
configcrates:
  - nginx
```

`stacks/envoys/envoys.vgo`:

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

- **No Ruby.** Chef's entire stack runs on Ruby — cookbooks, recipes, attributes, templates. Vigo is pure YAML with Go templates in `content:` only.
- **One file per configcrate.** A Chef cookbook is a directory tree (`recipes/`, `templates/`, `attributes/`, `metadata.rb`, etc.). A Vigo configcrate is a single `.vgo` file.
- **No attribute levels.** Chef's `default`/`normal`/`override`/`force_override`/`automatic` at cookbook/role/environment/node levels gives 15 precedence tiers. Vigo has three: configcrate defaults → role vars → node vars.
- **Templates inline or in `templates/`.** Small templates go directly in `content:`. For larger files, use `source: templates/nginx.conf.tmpl` — the server reads the file from the `templates/` directory and renders it with the same Go template engine. No `templates/default/` subdirectory convention.

## Variable and Data Hierarchy

Chef attributes have 15 precedence levels. In practice, most teams use `default` and `override`. Vigo maps this cleanly:

| Chef Level | Vigo Equivalent |
|---|---|
| `default` (cookbook) | Configcrate `vars:` |
| `default` (role) | Role `vars:` |
| `default` (environment) | `common.vgo` vars at ancestor directories |
| `override` (node) | Match-block `vars:` |
| `override` (environment) | `environments.vgo` keyed by `environment:` |
| `automatic` (Ohai) | Traits (accessed via `.Traits` in templates, or `when:`) |
| Data bags | `common.vgo` at any directory level (shared via inheritance) |

Per-environment overrides live in `environments.vgo`, not per-match-block:

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

Chef uses ERB. Vigo uses Go templates.

| Chef ERB | Vigo Go Template |
|---|---|
| `<%= @variable %>` | `{{ .Vars.variable }}` |
| `<%= node['attr'] %>` | `{{ .Vars.attr }}` |

| `<%= node['fqdn'] %>` | `{{ .Traits.identity.fqdn }}` |
| `<% if condition %>` | `{{ if .Vars.condition }}` |
| `<% @items.each do \|i\| %>` | `{{ range .Vars.items }}` |

**Critical restriction:** Go templates work only in `content:` attributes. You cannot use `{{ }}` in `package:`, `target_path:`, `command:`, or any other attribute.

## Conditionals and Targeting

Chef conditionals:

```ruby
package 'nginx' do
  action :install
  only_if { node['platform_family'] == 'debian' }
end
```

Vigo equivalent:

```yaml
- name: nginx-package
  type: package
  package: nginx
  state: present
  when: "os_family('debian')"
```

Chef's `only_if`/`not_if` guards run arbitrary Ruby. Vigo's `when:` uses a fixed expression syntax with 15 builtins and boolean operators.

## Secrets

Chef uses data bags (optionally encrypted) or Chef Vault:

```ruby
db_password = data_bag_item('secrets', 'database')['password']
```

Vigo uses the `secret:` prefix:

```yaml
content: "secret:vigo/database/password"
```

The secrets provider resolves references at bundle compile time. No Ruby code, no key management ceremony.

## Common Gotchas

1. **No Ruby.** You cannot run arbitrary code during convergence. If Chef recipes contain procedural Ruby logic beyond resource declarations, you'll need to decompose it into declarative resources with `when:` conditionals.

2. **No compile phase.** Chef has a two-phase model (compile then converge). Vigo has no compile phase — resources are interpreted from YAML directly. There's no equivalent of `lazy {}` evaluation.

3. **Templates only in `content:`.** Chef lets you use ERB in any string and template helper methods everywhere. Vigo restricts Go templates to `content:`. Use `when:` and separate resources for conditional logic.

4. **Flat variable namespace.** Chef attributes are nested hashes (`node['nginx']['port']`). Vigo vars are flat strings (`nginx_port`). Prefix with the configcrate name to avoid collisions.

5. **No search.** Chef Server's search API (`search(:node, 'role:webserver')`) has no direct equivalent. For cross-node coordination, use directory-level `common.vgo` inheritance or the task/query system.

6. **No `run_list` manipulation.** Chef can dynamically include recipes. Vigo's configcrate list is static — defined in `envoys.vgo` role assignments. Use `when:` to conditionally skip entire configcrates.

7. **No Berkshelf/Policyfile dependency resolution.** Configcrates don't declare dependencies on other configcrates. Include all required configcrates in the role definition.

8. **First-match-wins node classification.** Chef can merge multiple roles and environments. Vigo's `envoys.vgo` uses first-match-wins — only the first matching glob applies.

## Related

- [Config Format](../../reference/configcrate-language.md) — Configcrates, roles, envoys.vgo, vars
- [Resource DAG](../../reference/configcrate-language.md) — depends_on, notify, subscribes
- [Templates](../../reference/configcrate-language.md) — Go template syntax and restrictions
- [When Expressions](../../reference/configcrate-language.md) — Conditional expressions and builtins
- [Secrets](../../reference/server-yaml.md) — Secret providers and the secret: prefix
- [Executors](../../reference/vigo/resources/_index.md) — All resource executors
- [Nginx Configcrate Example](../../examples/nginx-module.md) — Full nginx walkthrough
- [First Configcrate](../../howto/configcrates/write-your-first-configcrate.md) — Writing your first configcrate

---
*Verified on Vigo 0.51.6 · 2026-05-13.*
