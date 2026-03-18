# First Module

This walkthrough builds a complete nginx module with package installation, config file management, and service control — demonstrating the core patterns you'll use in every Hobnob module.

## Module Structure

A module is a YAML file with a `.hnb` extension in `policy/proposed/modules/`. It contains:

```yaml
name: module-name
vars:
  key: default_value
resources:
  - name: resource-name
    type: resource-type
    # type-specific attributes
```

## Step 1: Create the Module

Create `policy/proposed/modules/nginx.hnb`:

```yaml
name: nginx

vars:
  nginx_port: 80
  server_name: localhost

resources:
  - name: nginx-package
    type: package
    package: nginx
    state: present

  - name: nginx-config
    type: file
    target_path: /etc/nginx/sites-available/default
    content: |
      server {
          listen {{ .Vars.nginx_port }};
          server_name {{ .Vars.server_name }};
          root /var/www/html;
          index index.html;
      }
    owner: root
    group: root
    mode: "0644"
    depends_on: [nginx-package]
    notify: [nginx-service]

  - name: nginx-service
    type: service
    service: nginx
    state: running
    enabled: true
    depends_on: [nginx-package]
```

## Step 2: Understand the Resource Chain

This module demonstrates three key patterns:

**depends_on** — Ordering guarantee. `nginx-config` won't run until `nginx-package` succeeds. `nginx-service` also waits for `nginx-package`.

**notify** — Trigger on change. When `nginx-config` changes the file, it notifies `nginx-service` to restart. If the config file is unchanged, the service is not restarted.

**Templates** — The `content:` attribute uses Go template syntax. `{{ .Vars.nginx_port }}` is replaced with the value of the `nginx_port` variable at check-in time.

![Resource Dependency Graph](img/resource-dag.svg)

## Step 3: Assign to a Thrall

Create a directory for your web servers and add a thrall entry:

```bash
mkdir -p policy/proposed/web
```

```yaml
# policy/proposed/web/web.hnb
thralls:
  - match: "web*.example.com"
    modules: [nginx]
    vars:
      nginx_port: 8080
      server_name: "web.example.com"
```

Node-level `vars` override the module's default `vars`. Here, `nginx_port` becomes `8080` instead of the default `80`.

**Tip:** If you have modules that should apply to *all* your thralls (like SSH and monitoring), put them in `policy/proposed/common.hnb` — everything in subdirectories inherits them automatically. See [Directory Inheritance](../concepts/config-format.md#directory-inheritance).

## Step 4: Use Roles for Reuse

For multiple modules, group them into a role. Create `policy/proposed/roles.hnb`:

```yaml
roles:
  - name: webserver
    modules:
      - nginx
      - logrotate-nginx
      - monitoring-node-exporter
```

Then assign the role:

```yaml
thralls:
  - match: "web*.example.com"
    roles: [webserver]
    vars:
      nginx_port: 8080
```

## Step 5: Environment-Specific Config

Use `environment_overrides` to vary config by environment:

```yaml
thralls:
  - match: "web*.example.com"
    environment: production
    roles: [webserver]
    vars:
      nginx_port: 80
    environment_overrides:
      staging:
        nginx_port: 8080
      production:
        nginx_port: 443
```

When the thrall's environment is `production`, `nginx_port` resolves to `443`.

## Step 6: Conditional Resources

Use `when:` to apply resources conditionally:

```yaml
resources:
  - name: nginx-package-debian
    type: package
    package: nginx
    when: "os_family('debian')"

  - name: nginx-package-redhat
    type: package
    package: nginx
    when: "os_family('redhat')"
```

The agent evaluates `when:` expressions using the thrall's traits. Only the matching resource runs.

## Step 7: Publish and Push

Publish the config to make it active:

```bash
hobnobcli config publish
```

The publish command runs your module through the **module linter**, which:
- Fixes common YAML issues (tabs, trailing whitespace, unquoted booleans)
- Normalizes key ordering and indentation
- Validates required fields, cross-references, and idempotency
- Adds `#~` auto-comments describing each resource

After publishing, your module file will have `#~` comments like:

```yaml
#~ Manages package, file, service
name: nginx
...
resources:
  #~ Install nginx package
  - name: nginx-package
    ...
```

These `#~` comments are regenerated on every publish — don't edit them. Use plain `#` comments for your own notes.

Then force an immediate check-in:

```bash
hobnobcli push --target "web1.example.com"
```

Check the run result:

```bash
hobnobcli runs list --limit 1
```

View the thrall's applied config:

```bash
hobnobcli thralls show web1.example.com
```

## What's Next

- [Config Format](../concepts/config-format.md) — Full reference for modules, roles, vars, environment_overrides
- [Resource DAG](../concepts/resource-dag.md) — depends_on, notify, subscribes in depth
- [When Expressions](../concepts/when-expressions.md) — Conditional logic reference
- [Templates](../concepts/templates.md) — Go template syntax for content: attributes
- [File Executor: stream_edit](../executors/file.md#stream-edit) — Pipe file content through transform scripts before writing
