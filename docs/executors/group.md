# group

Manages local groups -- create, modify, and delete groups.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `group` | Yes | -- | Group name. Also accepts `name`. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `gid` | No | -- | Desired GID (group ID). |

## States

- **`present`** -- Ensure the group exists. If `gid` is specified and differs, modify it with `groupmod`.
- **`absent`** -- Delete the group.

## Idempotency

Reads `/etc/group` to check if the group exists and its current GID. Only creates or modifies when drift is detected.

## Examples

### Basic

```yaml
resources:
  - name: docker group
    type: group
    group: docker
```

### With specific GID

```yaml
resources:
  - name: myapp group
    type: group
    group: myapp
    gid: "1500"
```

### With depends_on

```yaml
resources:
  - name: app-group
    type: group
    group: myapp

  - name: app-user
    type: user
    username: myapp
    group: myapp
    depends_on: app-group
```

### Remove a group

```yaml
resources:
  - name: old-group
    type: group
    group: old-group
    state: absent
```

## Platform

Linux only.

## Notes

- Uses `groupadd`, `groupmod`, and `groupdel` commands.
