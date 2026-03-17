# ini

Manages key-value pairs in INI-style configuration files with optional section support.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `target_path` | Yes | -- | Absolute path to the INI file. |
| `section` | No | -- | Section name (e.g., `database`). If omitted, the key is managed in the global scope (before any section). |
| `key` | Yes | -- | The key to manage. |
| `value` | Yes (for present) | -- | The value to set. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `separator` | No | `=` | The separator between key and value (e.g., `=`, ` = `, `:`). |

## States

- **`present`** -- Ensure the key exists with the specified value in the given section.
- **`absent`** -- Remove the key from the section.

## Idempotency

Reads the file, parses sections and key-value pairs, and checks whether the key already has the desired value. Only writes when the value differs or the key needs to be added/removed.

## Examples

### Basic

```yaml
resources:
  - name: Set database host
    type: ini
    target_path: /etc/myapp/config.ini
    section: database
    key: host
    value: db.internal
```

### Global key (no section)

```yaml
resources:
  - name: Set app mode
    type: ini
    target_path: /etc/myapp/config.ini
    key: mode
    value: production
```

### Custom separator

```yaml
resources:
  - name: Set syslog config
    type: ini
    target_path: /etc/rsyslog.d/50-default.conf
    key: MaxMessageSize
    value: "8192"
    separator: " "
```

### Remove a key

```yaml
resources:
  - name: Remove deprecated setting
    type: ini
    target_path: /etc/myapp/config.ini
    section: database
    key: legacy_mode
    state: absent
```

## Platform

Cross-platform.

## Notes

- Section headers are matched as `[section_name]`.
- If the section does not exist and `state: present`, the section is created.
- Comments and blank lines in the file are preserved.
