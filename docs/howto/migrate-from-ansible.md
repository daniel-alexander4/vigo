
# Migrating from Ansible

Ansible pushes configuration over SSH from a control node; Vigo runs a persistent agent on each envoy that pulls desired state from a central server. The biggest practical shifts when porting an Ansible setup: execution model (push → pull), template scope (Jinja2 everywhere → Go templates inside `content:` only), and inventory shape (`host_vars` / `group_vars` → matchfiles + roles + per-host vars).

## Architecture Differences

Ansible pushes configuration over SSH from a control node. There is no persistent agent — each run connects, executes tasks, and disconnects. Vigo runs a persistent agent on each managed node that pulls desired state from a central server over gRPC/mTLS on a configurable interval.

Ansible processes tasks sequentially in playbook order. Vigo builds a resource DAG from `depends_on`, `notify`, and `subscribes` declarations, then executes resources respecting the dependency graph.

Ansible is agentless but requires SSH access and Python on target hosts. Vigo's agent is a single static Rust binary with no runtime dependencies — it handles its own TLS, state caching, and offline convergence.

Ansible uses Jinja2 everywhere — in templates, task arguments, conditionals, variable files. Vigo restricts Go templates to `content:` attributes only. All other fields are literal values.

## Concept Mapping

| Ansible Concept | Vigo Equivalent | Notes |
|---|---|---|
| Playbook | `envoys.vgo` + roles | Node-to-role assignment replaces playbook targeting |
| Role | Module + Role | An Ansible role maps to a Vigo module; a Vigo role groups modules |
| Task | Resource | Declarative, idempotent unit of work |
| Handler | `notify` / `subscribes` | Same semantics — triggered on change, run at end |
| Inventory | `envoys.vgo` | Glob patterns instead of host groups |
| `host_vars` | Node-level `vars:` | Per-host overrides in `envoys.vgo` |
| `group_vars` | Module `vars:` / Role `vars:` | Default values at module or role level |
| Facts (`ansible_*`) | Traits | Discovered automatically by the agent |
| `gather_facts` | Always-on trait collection | Traits collected every check-in — no toggle |
| Template (`.j2`) | `content:` attribute | Inline Go template, no separate file |
| `when:` | `when:` | Same keyword, different syntax (no Jinja2) |
| `loop` / `with_items` | `foreach:` | Iterates over a list or map to stamp out resources |
| `become: yes` | Agent runs as root | No sudo — agent runs as root by design |
| `register:` | No equivalent | Resources don't capture output for later use |
| Vault | `secret:` prefix | Resolved server-side at bundle compile time |
| `ansible-pull` | Default mode | Vigo is always pull — this is the primary model |
| Galaxy role | Module `.vgo` file | No package registry — YAML files in version control |
| `tags:` | No equivalent | All resources in a module always apply (use `when:` to conditionally skip) |
| `delegate_to:` | Tasks / Workflows | Cross-node orchestration uses the task system |
| `--check` mode | Dry-run in check phase | Each resource checks before acting — inherent to the model |

## Resource Type Mapping

| Ansible Module | Vigo Executor | Notes |
|---|---|---|
| `ansible.builtin.package` | `package` | Auto-detects package manager |
| `ansible.builtin.copy` / `template` | `file` | `content:` for templates, `sourcepath:` for copies |
| `ansible.builtin.service` | `service` | `state: running/stopped`, `enabled: true/false` |
| `ansible.builtin.command` / `shell` | `exec` | `command:`, `onlyif:`, `unless:` |
| `ansible.builtin.user` | `user` | `username:`, `uid:`, `shell:`, `groups:` |
| `ansible.builtin.group` | `group` | `groupname:`, `gid:`, `members:` |
| `ansible.builtin.cron` | `cron` | `command:`, `minute:`, `hour:`, etc. |
| `ansible.builtin.lineinfile` | `file_line` | `path:`, `line:`, `match:` |
| `ansible.builtin.apt_repository` | `repository` | Unified across distros |
| `ansible.builtin.get_url` / `unarchive` | `source_package` | `url:`, `target_path:`, `checksum:` (tarballs/zips) |
| `ansible.builtin.apt` (with `deb:`) / `dnf` (with URL) | `nonrepo_package` | Install .deb/.rpm/.pkg/.msi from URLs |
| `ansible.posix.mount` | `mount` | `device:`, `mountpoint:`, `fstype:` |
| `ansible.posix.sysctl` | `sysctl` | `key:`, `value:`, `state: present` |
| `ansible.builtin.hostname` | `hostname` | `hostname:` |
| `ansible.builtin.blockinfile` | `blockinfile` | `path:`, `block:`, `marker:` |

## Walkthrough: Converting an Nginx Role


### Ansible Version

`roles/nginx/defaults/main.yml`:

```yaml
nginx_worker_processes: auto
nginx_worker_connections: 1024
nginx_server_name: localhost
nginx_root: /var/www/html
```

`roles/nginx/tasks/main.yml`:

```yaml
- name: Install nginx
  ansible.builtin.package:
    name: nginx
    state: present

- name: Configure nginx
  ansible.builtin.template:
    src: nginx.conf.j2
    dest: /etc/nginx/nginx.conf
    owner: root
    group: root
    mode: "0644"
  notify: Restart nginx

- name: Ensure nginx is running
  ansible.builtin.service:
    name: nginx
    state: started
    enabled: true
```

`roles/nginx/handlers/main.yml`:

```yaml
- name: Restart nginx
  ansible.builtin.service:
    name: nginx
    state: restarted
```

`roles/nginx/templates/nginx.conf.j2`:

```jinja2
worker_processes {{ nginx_worker_processes }};
events {
    worker_connections {{ nginx_worker_connections }};
}
http {
    include       /etc/nginx/mime.types;
    default_type  application/octet-stream;
    sendfile      on;
    keepalive_timeout 65;

    server {
        listen 80;
        server_name {{ nginx_server_name }};
        root {{ nginx_root }};

        location / {
            try_files $uri $uri/ =404;
        }
    }
}
```

### Vigo Version

`stockpile/modules/nginx.vgo`:

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
modules:
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

- **Templates inline or in `templates/`.** Small templates go inline in `content:`. Larger files use `source: templates/nginx.conf.tmpl` — the server reads and renders the file from the `templates/` directory. Same concept as Ansible's `templates/`, but no Jinja2.
- **No handlers directory.** Ansible's handler/notify pattern maps directly to Vigo's `notify:` list on the resource. No separate `handlers/main.yml` file.
- **No defaults directory.** Module-level `vars:` replaces `defaults/main.yml`. Same concept, inline in the module file.
- **One file per module.** An Ansible role is 4+ files across multiple directories. A Vigo module is a single `.vgo` file.
- **Template syntax.** `{{ variable }}` in Jinja2 becomes `{{ .Vars.variable }}` in Go templates. Note the `.Vars.` prefix.

## Variable and Data Hierarchy

Ansible has a famously complex [variable precedence](https://docs.ansible.com/ansible/latest/playbook_guide/playbooks_variables.html#understanding-variable-precedence) with 22 levels. Vigo has three:

1. **Module defaults** — `vars:` in the module `.vgo` file
2. **Inherited common.vgo vars** — shared across the directory subtree
3. **Match-block vars** — `vars:` in the `envoys.vgo` entry
4. **`environments.vgo` vars** — selected by match-block `environment:`

Later layers override earlier ones. Env-specific vars live in a sibling file, not inline on the match block:

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

For data shared across many match blocks, use `common.vgo` at a parent directory — directory inheritance replaces Ansible's `group_vars/` + `vars_files:` patterns.

## Templates

Ansible uses Jinja2 templates everywhere. Vigo uses Go templates in `content:` only.


| Ansible Jinja2 | Vigo Go Template |
|---|---|
| `{{ variable }}` | `{{ .Vars.variable }}` |
| `{{ ansible_hostname }}` | `{{ .Traits.identity.hostname }}` |
| `{{ ansible_os_family }}` | `{{ .Traits.os.family }}` |
| `{% if condition %}` | `{{ if .Vars.condition }}` |
| `{% for item in list %}` | `{{ range .Vars.list }}` |
| `{{ variable \| lower }}` | `{{ lower .Vars.variable }}` |
| `{{ variable \| default('x') }}` | `{{ default .Vars.variable "x" }}` |

**Critical restriction:** Vigo templates work only inside `content:` attributes. You cannot use `{{ }}` in `package:`, `target_path:`, `command:`, or any other field. For dynamic behavior in non-content fields, use `when:` expressions with separate resources.

## Conditionals and Targeting

Ansible conditionals use Jinja2 syntax:

```yaml
- name: Install nginx (Debian)
  ansible.builtin.apt:
    name: nginx
    state: present
  when: ansible_os_family == "Debian"
```

Vigo uses `when:` with builtin functions:

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

Ansible's `when:` accepts full Jinja2 expressions. Vigo's `when:` uses its own expression syntax with 15 builtins and boolean operators (`&&`, `||`, `!`).

## Secrets

Ansible Vault encrypts entire files or individual variables:

```bash
ansible-vault encrypt_string 'supersecret' --name 'db_password'
```

Vigo uses the `secret:` prefix in any value:

```yaml
content: "secret:vigo/nginx/ssl_key"
```

Secrets are resolved server-side at bundle compile time. The encrypted value never appears in configs, logs, or agent traffic. No vault password file to manage.

## Common Gotchas


1. **No push mode.** Ansible pushes on demand. Vigo agents pull on an interval. For immediate convergence, use `vigocli envoys push` to bump specific envoys, or use `vigocli task run` for ad-hoc commands.

2. **No `register:` / variable chaining.** Ansible can capture task output and use it in later tasks. Vigo resources are declarative and don't produce variables. If you need dynamic values, use traits (for system facts) or directory-level `common.vgo` inheritance (for shared data).

3. **Templates only in `content:`.** Ansible's Jinja2 works everywhere — task arguments, `when:`, variable files. Vigo restricts templates to `content:`. Resist the urge to template `target_path:` or `command:`.

4. **No task ordering by file position.** Ansible runs tasks in the order they appear. Vigo builds a DAG — without explicit `depends_on` or `notify`, resources may execute in any order. Always declare dependencies explicitly.

5. **No `become:` / sudo.** The Vigo agent runs as root. There's no privilege escalation model — if the agent needs to do it, it does it as root.

6. **No `--limit` or `--tags`.** Ansible can target subsets of hosts or tasks per run. Vigo applies all matching resources every check-in. Use `when:` to conditionally skip resources, and glob patterns in `envoys.vgo` for host targeting.

7. **No ad-hoc `ansible` command.** For one-off commands across the fleet, use `vigocli task run` or `vigocli query run` — these dispatch through the agent stream.

8. **Agent must be installed first.** Ansible needs only SSH. Vigo requires bootstrapping the agent binary on each node. Use `curl -sSfk https://<server>:8443/bootstrap | sudo sh` for automated enrollment.

## Related

- [Config Format](../reference/writing-modules.md) — Modules, roles, envoys.vgo, vars
- [Resource DAG](../reference/writing-modules.md) — depends_on, notify, subscribes
- [Templates](../reference/writing-modules.md) — Go template syntax and restrictions
- [When Expressions](../reference/writing-modules.md) — Conditional expressions and builtins
- [Secrets](../reference/server-yaml.md) — Secret providers and the secret: prefix
- [Executors](../reference/vigo/resources/_index.md) — All resource executors
- [Nginx Module Example](../examples/nginx-module.md) — Full nginx walkthrough
- [First Module](../howto/write-your-first-module.md) — Writing your first module
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../legal/license.md).*
