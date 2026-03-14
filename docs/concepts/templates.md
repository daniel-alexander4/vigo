# Templates

Hobnob uses Go template syntax for dynamic content in resource definitions. Templates are **only allowed in `content:` attributes** — they are rendered server-side at check-in time.

## Where Templates Are Allowed

| Attribute | Templates Allowed |
|-----------|:-:|
| `content:` | Yes |
| `targetpath:` | No |
| `package:` | No |
| `service:` | No |
| `command:` | No |
| `owner:`, `group:` | No |
| `mode:` | No |
| `url:`, `repo:` | No |
| `stream_edit:` | No |
| `when:` | No |
| All other attributes | No |

For attributes that need to vary per-thrall, use `vars` with literal overrides at the node level instead of templates.

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
    targetpath: /etc/app/config.yaml
    content: |
      name: {{ .Vars.app_name }}
      port: {{ .Vars.app_port }}
```

### .Traits

Thrall traits (auto-discovered facts):

```yaml
resources:
  - name: motd
    type: file
    targetpath: /etc/motd
    content: |
      ==========================================
      Hostname: {{ .Traits.hostname }}
      OS:       {{ .Traits.os.name }} {{ .Traits.os.version }}
      Arch:     {{ .Traits.os.arch }}
      CPUs:     {{ .Traits.processors.count }}
      Memory:   {{ .Traits.memory.total_mb }} MB
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
{{ .Traits.os.name }}
{{ .Traits.network.ipv4 }}
{{ index .Traits.network "interfaces" }}
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
  targetpath: /etc/ssh/sshd_config
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
  targetpath: /etc/nginx/sites-available/app
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
  targetpath: /etc/hobnob-node-info
  content: |
    hostname={{ .Traits.hostname }}
    os={{ .Traits.os.name }}
    version={{ .Traits.os.version }}
    arch={{ .Traits.os.arch }}
    ip={{ .Traits.network.ipv4 }}
    managed_by=hobnob
```

## Common Mistakes

**Template in targetpath (won't work):**
```yaml
# WRONG — templates not allowed in targetpath
- name: config
  type: file
  targetpath: "/etc/{{ .Vars.app_name }}/config.yaml"
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

- [Config Format](config-format.md) — Vars and env_vars
- [Trait Collectors](../traits/_index.md) — Available .Traits data
- [Executors](../executors/_index.md) — Which attributes each executor uses
