# user

Manages local user accounts -- create, modify, and delete users with configurable shell, home directory, groups, and SSH authorized keys.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `username` | Yes | -- | Username. Also accepts `name`. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `uid` | No | -- | Desired UID. |
| `shell` | No | -- | Login shell (e.g., `/bin/bash`). |
| `home` | No | -- | Home directory path. |
| `comment` | No | -- | User comment / GECOS field. |
| `group` | No | -- | Primary group. |
| `groups` | No | -- | Comma-separated supplementary groups. |
| `password` | No | -- | Hashed password string. |
| `authorized_keys` | No | -- | SSH authorized keys content (written to `~/.ssh/authorized_keys`). |
| `system` | No | `false` | If `true`, create a system account (`useradd --system`). |

## States

- **`present`** -- Ensure the user exists with the specified attributes. Creates with `useradd` if missing, modifies with `usermod` if attributes differ.
- **`absent`** -- Delete the user and optionally their home directory (`userdel --remove`).

## Idempotency

Reads `/etc/passwd` to check if the user exists and compares each attribute (UID, shell, home, groups, etc.). Only runs `useradd` or `usermod` when attributes need changing.

## Examples

### Basic

```yaml
resources:
  - name: deploy user
    type: user
    username: deploy
    shell: /bin/bash
    groups: "sudo,docker"
```

### System account

```yaml
resources:
  - name: myapp service account
    type: user
    username: myapp
    system: "true"
    shell: /usr/sbin/nologin
    home: /var/lib/myapp
```

### With authorized keys

```yaml
resources:
  - name: deploy user
    type: user
    username: deploy
    shell: /bin/bash
    authorized_keys: |
      ssh-ed25519 AAAA... admin@example.com
      ssh-ed25519 AAAA... ci@example.com
```

### Remove a user

```yaml
resources:
  - name: old-user
    type: user
    username: old-user
    state: absent
```

## Platform

Linux only. On Windows, the same `type: user` maps to the [user_windows](user_windows.md) executor.

## Notes

- Uses `useradd`, `usermod`, and `userdel` commands.
- The `authorized_keys` content is written to `<home>/.ssh/authorized_keys` with mode 0600, and the `.ssh` directory is created with mode 0700 if needed.
- When `state: absent`, the user is deleted with `userdel --remove` (which also removes the home directory).
