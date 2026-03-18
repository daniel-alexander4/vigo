# Migrating from Chef

Hobnob and Chef share the pull-based model and converge-on-interval pattern. The biggest shift is from Ruby DSL to plain YAML, and from Chef's 15-level attribute precedence to three levels. If you've managed infrastructure with Chef, Hobnob's resource model will be instantly recognizable.

## Architecture Differences

Both Chef and Hobnob use a pull-based agent that contacts a server on a regular interval, receives desired state, and converges locally. Chef compiles Ruby code into a resource collection; Hobnob interprets YAML directly — there's no compilation step.

Chef's infrastructure includes a Chef Server (or Chef Automate), client nodes running chef-client (Ruby), and often a Supermarket for cookbook distribution. Hobnob has a Go server (hobd), a Rust agent (hobnob), and a CLI (hobnobcli). No Ruby runtime, no package manager, no external services.

Chef stores node objects and search indexes on the server (PostgreSQL + Elasticsearch in Chef Server). Hobnob uses an in-memory FleetIndex backed by SQLite — no external database.

Chef's attribute system has 15 precedence levels (default, force_default, normal, override, force_override, automatic — each at role, environment, cookbook, and recipe levels). Hobnob has three levels: module defaults, role vars, node vars.

## Concept Mapping

| Chef Concept | Hobnob Equivalent | Notes |
|---|---|---|
| Cookbook | Module (`.hnb` file) | Single YAML file replaces the cookbook directory tree |
| Recipe | Module resources | Resources listed in the module — no separate recipe files |
| Role | Role | Named list of modules — same concept |
| Run list | Module list in `nodes.hnb` | Roles and modules assigned to matching thralls |
| Node object | FleetIndex entry + Traits | In-memory, not stored server-side as a document |
| Ohai | Traits | System facts collected by the agent |
| Attributes (default) | Module `vars:` | Default values in the module |
| Attributes (override) | Node `vars:` in `nodes.hnb` | Node-level overrides |
| Data bag | `vars_from:` | External YAML data loaded at check-in |
| Chef Vault | `secret:` prefix | Resolved server-side, never in plaintext |
| Environment | `environment_overrides:` in `nodes.hnb` | Environment-specific variable overrides |
| `notifies` | `notify` | Trigger downstream resource on change |
| `subscribes` | `subscribes` | Same keyword, same semantics |
| `only_if` / `not_if` | `when:` | Resource conditionals |
| `include_recipe` | Module dependencies | Modules listed in roles or `depends_on` |
| `knife` | `hobnobcli` | CLI for server administration |
| Chef Supermarket | No equivalent | Modules are plain YAML files — no registry |
| Policyfile | `nodes.hnb` | Pin module versions at the node level |
| `chef-client` | `hobnob` agent | Rust binary, LMDB state, offline convergence |
| `chef-server` | `hobd` | Go binary, gRPC + REST + Web UI |
| ERB template | Go template in `content:` | Templates only in `content:` attribute |
| `knife bootstrap` | `hobnobcli tokens` + bootstrap script | One-time token enrollment |

## Resource Type Mapping

| Chef Resource | Hobnob Executor | Notes |
|---|---|---|
| `package` | `package` | Auto-detects package manager |
| `template` / `cookbook_file` | `file` | `content:` for templates, `sourcepath:` for static files |
| `service` | `service` | `state: running/stopped`, `enabled: true/false` |
| `execute` / `bash` | `exec` | `command:`, `creates:`, `onlyif:`, `unless:` |
| `user` | `user` | `username:`, `uid:`, `shell:`, `groups:` |
| `group` | `group` | `groupname:`, `gid:`, `members:` |
| `cron` | `cron` | `command:`, `minute:`, `hour:`, etc. |
| `apt_repository` / `yum_repository` | `repository` | Unified across distros |
| `remote_file` | `archive` | `url:`, `target_path:`, `checksum:` |
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

### Hobnob Version

`policy/proposed/modules/nginx.hnb`:

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

`policy/proposed/roles/webserver.hnb`:

```yaml
name: webserver
modules:
  - nginx
```

`policy/proposed/thralls/nodes.hnb`:

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

- **No Ruby.** Chef's entire stack runs on Ruby — cookbooks, recipes, attributes, templates. Hobnob is pure YAML with Go templates in `content:` only.
- **One file per module.** A Chef cookbook is a directory tree (`recipes/`, `templates/`, `attributes/`, `metadata.rb`, etc.). A Hobnob module is a single `.hnb` file.
- **No attribute levels.** Chef's `default`/`normal`/`override`/`force_override`/`automatic` at cookbook/role/environment/node levels gives 15 precedence tiers. Hobnob has three: module defaults → role vars → node vars.
- **Templates inline or in `templates/`.** Small templates go directly in `content:`. For larger files, use `source: templates/nginx.conf.tmpl` — the server reads the file from the `templates/` directory and renders it with the same Go template engine. No `templates/default/` subdirectory convention.

## Variable and Data Hierarchy

Chef attributes have 15 precedence levels. In practice, most teams use `default` and `override`. Hobnob maps this cleanly:

| Chef Level | Hobnob Equivalent |
|---|---|
| `default` (cookbook) | Module `vars:` |
| `default` (role) | Role `vars:` |
| `override` (node/environment) | Node `vars:` / `environment_overrides:` |
| `automatic` (Ohai) | Traits (accessed via `.Traits` in templates, or `when:`) |
| Data bags | `vars_from:` (loads external YAML) |

The `environment_overrides:` key provides environment-specific overrides without separate environment objects:

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

Chef uses ERB. Hobnob uses Go templates.

| Chef ERB | Hobnob Go Template |
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

Hobnob equivalent:

```yaml
- name: nginx-package
  type: package
  package: nginx
  state: present
  when: "os_family('debian')"
```

Chef's `only_if`/`not_if` guards run arbitrary Ruby. Hobnob's `when:` uses a fixed expression syntax with 15 builtins and boolean operators.

## Secrets

Chef uses data bags (optionally encrypted) or Chef Vault:

```ruby
db_password = data_bag_item('secrets', 'database')['password']
```

Hobnob uses the `secret:` prefix:

```yaml
content: "secret:hobnob/database/password"
```

The secrets provider resolves references at bundle compile time. No Ruby code, no key management ceremony.

## Common Gotchas

1. **No Ruby.** You cannot run arbitrary code during convergence. If Chef recipes contain procedural Ruby logic beyond resource declarations, you'll need to decompose it into declarative resources with `when:` conditionals.

2. **No compile phase.** Chef has a two-phase model (compile then converge). Hobnob has no compile phase — resources are interpreted from YAML directly. There's no equivalent of `lazy {}` evaluation.

3. **Templates only in `content:`.** Chef lets you use ERB in any string and template helper methods everywhere. Hobnob restricts Go templates to `content:`. Use `when:` and separate resources for conditional logic.

4. **Flat variable namespace.** Chef attributes are nested hashes (`node['nginx']['port']`). Hobnob vars are flat strings (`nginx_port`). Prefix with the module name to avoid collisions.

5. **No search.** Chef Server's search API (`search(:node, 'role:webserver')`) has no direct equivalent. For cross-node coordination, use `vars_from:` or the task/query system.

6. **No `run_list` manipulation.** Chef can dynamically include recipes. Hobnob's module list is static — defined in `nodes.hnb` role assignments. Use `when:` to conditionally skip entire modules.

7. **No Berkshelf/Policyfile dependency resolution.** Modules don't declare dependencies on other modules. Include all required modules in the role definition.

8. **First-match-wins node classification.** Chef can merge multiple roles and environments. Hobnob's `nodes.hnb` uses first-match-wins — only the first matching glob applies.

## Related

- [Config Format](../concepts/config-format.md) — Modules, roles, nodes.hnb, vars
- [Resource DAG](../concepts/resource-dag.md) — depends_on, notify, subscribes
- [Templates](../concepts/templates.md) — Go template syntax and restrictions
- [When Expressions](../concepts/when-expressions.md) — Conditional expressions and builtins
- [Secrets](../concepts/secrets.md) — Secret providers and the secret: prefix
- [Executors](../executors/_index.md) — All resource executors
- [Nginx Module Example](../examples/nginx-module.md) — Full nginx walkthrough
- [First Module](../getting-started/first-module.md) — Writing your first module
