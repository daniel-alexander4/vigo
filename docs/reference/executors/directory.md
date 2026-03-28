
# directory

Creates and removes directories with specified permissions and ownership.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `target_path` | Yes | -- | Absolute path to the directory. |
| `mode` | No | `0755` | Directory permission mode (octal string). |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `owner` | No | -- | Directory owner (username or UID). Linux only. |
| `group` | No | -- | Directory group (group name or GID). Linux only. |
| `recurse` | No | `false` | When `true`, recursively apply `mode`, `owner`, and `group` to all files and subdirectories. |

## States

- **`present`** -- Ensure the directory exists with the specified mode, owner, and group. Creates parent directories as needed (`mkdir -p` behavior).
- **`absent`** -- Remove the directory and all its contents recursively.

## Idempotency

Checks if the directory exists and whether its current mode, owner, and group match the desired values. Only creates or modifies when there is drift.

## Examples

### Basic

```yaml
resources:
  - name: /opt/myapp
    type: directory
    target_path: /opt/myapp
    mode: "0755"
    owner: deploy
    group: deploy
```

### With depends_on

```yaml
resources:
  - name: deploy-user
    type: user
    username: deploy

  - name: /opt/myapp
    type: directory
    target_path: /opt/myapp
    owner: deploy
    depends_on: deploy-user
```

### Recursive permissions

```yaml
resources:
  - name: /opt/myapp
    type: directory
    target_path: /opt/myapp
    mode: "0755"
    owner: deploy
    group: deploy
    recurse: "true"
```

### Absent

```yaml
resources:
  - name: /opt/old-app
    type: directory
    target_path: /opt/old-app
    state: absent
```

## Platform

Cross-platform. Owner/group setting requires Unix (Linux/macOS).

## Notes

- Uses `create_dir_all` for recursive creation and `remove_dir_all` for recursive deletion.
- Mode is applied after creation on Linux.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
