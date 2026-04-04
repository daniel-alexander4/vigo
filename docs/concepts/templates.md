
# Templates

Vigo uses Go template syntax for dynamic content in resource definitions. Templates are rendered agent-side during policy evaluation.

## Where Templates Are Allowed

Templates are rendered in **all string attributes** on every resource. This includes `content:`, `source:`, `target_path:`, `command:`, `repo:`, `key_url:`, `url:`, `owner:`, `group:`, `mode:`, `when:`, and any other string field.

```yaml
# All of these support {{ .Traits.* }} and {{ .Vars.* }}
- name: docker-repo
  type: repository
  key_url: "https://download.docker.com/linux/{{ .Traits.os.upstream_id }}/gpg"
  repo: "deb https://download.docker.com/linux/{{ .Traits.os.upstream_id }} {{ .Traits.os.upstream_codename }} stable"

- name: install-consul
  type: source_package
  url: "https://releases.hashicorp.com/consul/{{ .Vars.consul_version }}/consul_{{ .Vars.consul_version }}_linux_{{ .Traits.os.arch }}.zip"
```

For small templates, use `content:` directly. For larger config files, put the template in the `templates/` directory and reference it with `source:`. The `content:` and `source:` attributes are mutually exclusive.

## Template Data

Templates have access to two namespaces:

### .Vars

Module variables (after resolution):

```yaml
vars:
  app_port: 8080
  app_name: myapp

resources:
  - name: app-config
    type: file
    target_path: /etc/app/config.yaml
    content: |
      name: {{ .Vars.app_name }}
      port: {{ .Vars.app_port }}
```

### .Traits

Envoy traits (auto-discovered facts):

```yaml
resources:
  - name: motd
    type: file
    target_path: /etc/motd
    content: |
      ==========================================
      Hostname: {{ .Traits.network.hostname }}
      OS:       {{ .Traits.os.distro }} {{ .Traits.os.version }}
      Arch:     {{ .Traits.os.arch }}
      CPUs:     {{ .Traits.hardware.cpu_count }}
      Memory:   {{ .Traits.hardware.memory_mb }} MB
      ==========================================
```

## Go Template Syntax

### Variable Output

```
{{ .Vars.key }}
{{ .Traits.os.family }}
```

### Conditionals

```
{{ if eq .Traits.os.family "debian" }}
apt is the package manager
{{ else }}
yum/dnf is the package manager
{{ end }}
```

### Iteration

```
{{ range .Vars.allowed_users }}
AllowUser {{ . }}
{{ end }}
```

Where `allowed_users` is a list variable:

```yaml
vars:
  allowed_users: [alice, bob, charlie]
```

### Nested Map Access

```
{{ .Traits.os.distro }}
{{ index .Traits.network.ip_addresses 0 }}
{{ .Traits.network.fqdn }}
```

### Default Values

```
{{ .Vars.log_level | default "info" }}
```

Note: Go templates don't have a built-in `default` filter. Use `if` instead:

```
{{ if .Vars.log_level }}{{ .Vars.log_level }}{{ else }}info{{ end }}
```

## Examples


### SSH Configuration

```yaml
- name: sshd-config
  type: file
  target_path: /etc/ssh/sshd_config
  content: |
    Port {{ .Vars.ssh_port }}
    PermitRootLogin no
    PasswordAuthentication no
    {{ range .Vars.allowed_users }}
    AllowUsers {{ . }}
    {{ end }}
  notify: [sshd-service]
```

### Nginx Virtual Host

```yaml
- name: vhost
  type: file
  target_path: /etc/nginx/sites-available/app
  content: |
    server {
        listen {{ .Vars.nginx_port }};
        server_name {{ .Vars.server_name }};

        location / {
            proxy_pass http://127.0.0.1:{{ .Vars.app_port }};
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
        }
    }
```

### System Information

```yaml
- name: node-info
  type: file
  target_path: /etc/vigo-node-info
  content: |
    hostname={{ .Traits.network.hostname }}
    os={{ .Traits.os.distro }}
    version={{ .Traits.os.version }}
    arch={{ .Traits.os.arch }}
    ip={{ index .Traits.network.ip_addresses 0 }}
    managed_by=vigo
```

## Common Mistakes

**Template in targetpath (won't work):**
```yaml
# WRONG — templates not allowed in targetpath
- name: config
  type: file
  target_path: "/etc/{{ .Vars.app_name }}/config.yaml"
```

**Fix:** Use a literal path or set it via vars at the node level.

**Template in command (won't work):**
```yaml
# WRONG — templates not allowed in command
- name: restart
  type: exec
  command: "systemctl restart {{ .Vars.service_name }}"
```

**Fix:** Use vars to set the service name directly as a resource attribute.

## Related

- [Config Format](config-format.md) — Vars and environment_overrides
- [Trait Collectors](../reference/traits/_index.md) — Available .Traits data
- [Executors](../reference/executors/_index.md) — Which attributes each executor uses
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
