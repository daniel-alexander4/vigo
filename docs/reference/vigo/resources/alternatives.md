
# alternatives

Manages Debian/Ubuntu `update-alternatives` entries. Installs and sets the active alternative for a given name.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `name` | Yes | -- | Alternatives name (e.g., `editor`, `java`). Set by the resource's own `name:`; a second `name:` key in the same resource is a duplicate-key error. |
| `path` | Yes | -- | Path to the actual binary (e.g., `/usr/bin/vim`). |
| `state` | Yes | -- | Desired state: `present` or `absent`. |
| `link` | No | `/usr/bin/<name>` | Symlink location. Defaults to `/usr/bin/<name>`. |
| `priority` | No | `50` | Priority for this alternative. |

## States

- **`present`** -- Install the alternative and set it as active. Uses `update-alternatives --install` followed by `update-alternatives --set`.
- **`absent`** -- Remove the alternative with `update-alternatives --remove`.

## Idempotency

Queries the current alternative value using `update-alternatives --query`. If the current value matches the desired `path`, no action is taken. Falls back to reading the `/etc/alternatives/<name>` symlink.

## Examples

### Basic

```yaml
resources:
  - name: editor
    type: alternatives
    path: /usr/bin/vim
    priority: "100"
```

### Java alternative

```yaml
resources:
  - name: java
    type: alternatives
    path: /usr/lib/jvm/java-17-openjdk-amd64/bin/java
    link: /usr/bin/java
    priority: "200"
```

### Remove an alternative

```yaml
resources:
  - name: editor
    type: alternatives
    path: /usr/bin/nano
    state: absent
```

## Platform

Linux only (requires `update-alternatives`).

## Notes

- The `link` parameter defaults to `/usr/bin/<name>`, which is the standard location for most alternatives.
- When `state: present`, both `--install` and `--set` are called to ensure the specified path is the active alternative.
- Priority is used by the automatic mode of update-alternatives when no manual selection is made.
