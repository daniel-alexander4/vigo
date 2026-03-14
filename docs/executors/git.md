# git

Clones and updates git repositories idempotently. Supports branch selection, shallow clones, and forced updates.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `path` | Yes | -- | Local filesystem path for the repository. Also accepts `name`. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `repo` | Yes (for present) | -- | Remote repository URL. |
| `branch` | No | -- | Branch to check out. If the current branch differs, switches to this branch. |
| `depth` | No | -- | Clone depth for shallow clones (e.g., `1`). |
| `force` | No | `false` | If `true`, use `git reset --hard` instead of `git pull` when updating, and `git checkout --force` when switching branches. |

## States

- **`present`** -- Clone the repository if it does not exist, or update it if it does. Checks branch and remote commit hash for drift.
- **`absent`** -- Remove the repository directory.

## Idempotency

For existing repositories, the executor fetches from the remote and compares the local HEAD against the upstream tracking reference. If they match (and the branch is correct), no action is taken. For new clones, checks if the `.git` directory exists at the specified path.

## Examples

### Basic clone

```yaml
resources:
  - name: /opt/myapp
    type: git
    path: /opt/myapp
    repo: https://github.com/example/myapp.git
```

### Specific branch with shallow clone

```yaml
resources:
  - name: /opt/myapp
    type: git
    path: /opt/myapp
    repo: https://github.com/example/myapp.git
    branch: release-2.0
    depth: "1"
```

### Force update (discard local changes)

```yaml
resources:
  - name: /opt/myapp
    type: git
    path: /opt/myapp
    repo: https://github.com/example/myapp.git
    force: "true"
```

### With notify

```yaml
resources:
  - name: /opt/myapp
    type: git
    path: /opt/myapp
    repo: https://github.com/example/myapp.git
    notify: restart-myapp

  - name: restart-myapp
    type: exec
    command: systemctl restart myapp
    subscribes: /opt/myapp
```

### Remove a repository

```yaml
resources:
  - name: /opt/old-app
    type: git
    path: /opt/old-app
    state: absent
```

## Platform

Cross-platform. Requires `git` to be installed on the system.

## Notes

- When `force: true`, updates use `git reset --hard @{u}` instead of `git pull`, discarding any local changes.
- Branch switching fetches all remotes first (`git fetch --all`), then checks out the desired branch.
- The `depth` parameter only applies to initial clones, not updates.
