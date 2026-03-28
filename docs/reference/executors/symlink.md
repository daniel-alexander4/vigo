
# symlink

Manages symbolic and hard links idempotently.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `target_path` | Yes | -- | Path where the link will be created. |
| `to` | Yes (for present) | -- | Target path the link points to. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `link_type` | No | `soft` | Link type: `soft` (symbolic) or `hard`. |
| `force` | No | `false` | If `true`, remove an existing file/link at `target_path` before creating the new link. |
| `owner` | No | -- | Link owner (username or UID). Linux only. Applies to symlinks via `lchown`. |
| `group` | No | -- | Link group (group name or GID). Linux only. |

## States

- **`present`** -- Ensure the link exists and points to the correct target.
- **`absent`** -- Remove the link if it exists.

## Idempotency

For soft links, reads the current symlink target and compares it to the desired `to` value. For hard links, compares inode numbers of the link and target. Only acts when there is a mismatch.

## Examples

### Basic

```yaml
resources:
  - name: /usr/local/bin/myapp
    type: symlink
    target_path: /usr/local/bin/myapp
    to: /opt/myapp/bin/myapp
```

### Hard link

```yaml
resources:
  - name: /etc/nginx/sites-enabled/default
    type: symlink
    target_path: /etc/nginx/sites-enabled/default
    to: /etc/nginx/sites-available/default
    link_type: hard
```

### Force replace

```yaml
resources:
  - name: /usr/local/bin/python
    type: symlink
    target_path: /usr/local/bin/python
    to: /usr/bin/python3
    force: "true"
```

### Absent

```yaml
resources:
  - name: /usr/local/bin/old-tool
    type: symlink
    target_path: /usr/local/bin/old-tool
    state: absent
```

## Platform

Cross-platform. Hard links require the target file to exist. Owner/group requires Unix.

## Notes

- When `force: true` and a file (not a symlink) already exists at `target_path`, it is removed before creating the link.
- Symlink ownership is set via `lchown` (does not follow the link).
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
