# When Expressions

The `when:` attribute on resources and modules controls conditional execution. Expressions evaluate to true or false — the resource is skipped when false.

## Syntax

When expressions support boolean logic and function calls:

```yaml
when: "os_family('debian')"
when: "!is_container"
when: "os_family('debian') && arch('amd64')"
when: "file_exists('/opt/app') || dir_exists('/opt/app')"
when: "os_family('redhat') && !version_ge('9')"
```

## Operators

| Operator | Description | Example |
|----------|-------------|---------|
| `&&` | Logical AND | `os_family('debian') && arch('amd64')` |
| `\|\|` | Logical OR | `file_exists('/a') \|\| file_exists('/b')` |
| `!` | Logical NOT | `!is_container` |
| `(` `)` | Grouping | `(os_family('debian') \|\| os_family('redhat')) && arch('amd64')` |

## Builtin Functions (13)

All builtins are evaluated on the agent using local system state and traits.

### Filesystem

| Function | Args | Description |
|----------|------|-------------|
| `file_exists(path)` | 1 | True if path is a regular file |
| `dir_exists(path)` | 1 | True if path is a directory |

```yaml
- name: migrate-data
  type: exec
  command: "/opt/app/migrate.sh"
  when: "file_exists('/opt/app/migrate.sh')"
```

### Process

| Function | Args | Description |
|----------|------|-------------|
| `process_running(name)` | 1 | True if a process with this name exists |
| `command_succeeds(cmd)` | 1 | True if the command exits 0 |

```yaml
- name: stop-old-service
  type: exec
  command: "systemctl stop legacy-app"
  when: "process_running('legacy-app')"
```

### Time

| Function | Args | Description |
|----------|------|-------------|
| `hour_range(start, end)` | 2 | True if current local hour is within [start, end) |
| `minute_range(start, end)` | 2 | True if current local minute is within [start, end) |
| `day_of_week(day)` | 1 | True if current day matches (e.g., `monday`, `friday`) |
| `day_of_month(day)` | 1 | True if current day-of-month matches (1-31) |

```yaml
- name: maintenance-cleanup
  type: exec
  command: "/opt/cleanup.sh"
  when: "hour_range('2', '5') && day_of_week('sunday')"

- name: staggered-task
  type: exec
  command: "/opt/rotate-logs.sh"
  when: "minute_range('0', '15')"
```

### OS / Traits

| Function | Args | Description |
|----------|------|-------------|
| `os_family(family)` | 1 | True if `os.family` trait matches (case-insensitive) |
| `distro(name)` | 1 | True if `os.distro` trait matches (case-insensitive) |
| `arch(arch)` | 1 | True if `os.arch` trait matches |
| `version_ge(version)` | 1 | True if `os.version` trait is >= the given version |

```yaml
- name: apt-package
  type: package
  package: nginx
  when: "os_family('debian')"

- name: yum-package
  type: package
  package: nginx
  when: "os_family('redhat')"
```

### System

| Function | Args | Description |
|----------|------|-------------|
| `flag_set(flag)` | 1 | True if `/var/lib/hobnob/flags/<flag>` exists |
| `in_group(group)` | 1 | True if the system group exists |

```yaml
- name: enable-feature
  type: exec
  command: "/opt/app/enable-beta.sh"
  when: "flag_set('beta-features')"
```

Flags are a simple feature-gate mechanism. Create a flag:
```bash
mkdir -p /var/lib/hobnob/flags
touch /var/lib/hobnob/flags/beta-features
```

### Network

| Function | Args | Description |
|----------|------|-------------|
| `port_listening(port)` | 1 | True if the given TCP port is in LISTEN state (from `ports` trait) |

```yaml
- name: haproxy-backend
  type: file
  targetpath: /etc/haproxy/haproxy.cfg
  content: "..."
  when: "port_listening(8080)"
```

## Variables in When

When expressions can reference variables that resolve to boolean names. The server resolves variable names at check-in time:

```yaml
vars:
  is_debian:
    trait: os.family
    in: [debian]
  has_enough_ram:
    trait: memory.total_mb
    gte: 4096

resources:
  - name: heavy-service
    type: service
    service: analytics
    when: "is_debian && has_enough_ram"
```

Conditional vars (those with a `trait:` field) are resolved by the server and substituted as `true`/`false` before the expression reaches the agent. Builtin function calls are passed through to the agent for local evaluation.

## Evaluation Model

When expressions are evaluated in two phases:

```
Server (check-in time)              Agent (apply time)
┌──────────────────────┐           ┌──────────────────────┐
│ 1. Substitute vars   │           │ 3. Evaluate builtins │
│    is_debian → true  │──────────>│    file_exists(...)  │
│ 2. Short-circuit if  │           │    process_running() │
│    fully resolved    │           │    os_family(...)    │
└──────────────────────┘           └──────────────────────┘
```

If the expression is fully resolved after variable substitution (e.g., `true && true`), the server can filter the resource before sending it to the agent.

If the expression contains builtin function calls, it's passed to the agent for local evaluation.

## Module-Level When

Apply a `when:` to an entire module reference:

```yaml
# In a role definition
modules:
  - name: monitoring
    when: "!is_container"
```

Or in nodes.hnb:

```yaml
thralls:
  - match: "*"
    modules:
      - name: monitoring
        when: "!is_container"
```

## Related

- [Resource Language](resource-language.md) — conditional_block for grouping
- [Config Format](config-format.md) — Conditional vars
- [Trait Collectors](../traits/_index.md) — Available trait data for expressions
