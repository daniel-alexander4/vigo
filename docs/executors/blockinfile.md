# blockinfile

Manages marked blocks of text within a file, delimited by BEGIN/END markers. Useful for managing a section of a config file without touching the rest.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `path` | Yes | -- | Absolute path to the file. |
| `block` | Yes (for present) | -- | The content to place between the markers. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `label` | No | `managed` | Label used in marker comments to distinguish multiple blocks. |
| `marker_begin` | No | `# BEGIN {label} - managed by hobnob` | Custom begin marker. `{label}` is replaced with the label value. |
| `marker_end` | No | `# END {label} - managed by hobnob` | Custom end marker. |
| `create` | No | `false` | If `true`, create the file if it does not exist. |
| `mode` | No | -- | File permission mode when creating a new file. |

## States

- **`present`** -- Ensure the block exists between the markers. If the block already exists with the same content, no changes are made. If it exists with different content, it is updated.
- **`absent`** -- Remove the block and its markers from the file.

## Idempotency

Reads the file and looks for the BEGIN/END marker pair. Compares the content between the markers against the desired `block`. Only writes when the content differs or the markers are missing.

## Examples

### Basic

```yaml
resources:
  - name: SSH hardening config
    type: blockinfile
    path: /etc/ssh/sshd_config
    label: hardening
    block: |
      PermitRootLogin no
      PasswordAuthentication no
      MaxAuthTries 3
```

### With notify

```yaml
resources:
  - name: Nginx upstream block
    type: blockinfile
    path: /etc/nginx/conf.d/upstreams.conf
    label: app-upstream
    block: |
      upstream app {
        server 10.0.1.10:8080;
        server 10.0.1.11:8080;
      }
    create: "true"
    notify: reload-nginx
```

### Remove a block

```yaml
resources:
  - name: Remove old config block
    type: blockinfile
    path: /etc/myapp.conf
    label: deprecated-section
    state: absent
```

## Platform

Cross-platform.

## Notes

- The default markers look like `# BEGIN managed - managed by hobnob` and `# END managed - managed by hobnob`. Customize with `label`, `marker_begin`, and `marker_end`.
- When `create: true` and the file does not exist, it is created with the block and markers.
- Multiple blocks can coexist in the same file using different `label` values.
