
# user

Manages local user accounts -- create, modify, and delete users with configurable shell, home directory, groups, and SSH authorized keys.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `username` | Yes | -- | Username. |
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
| `manage_home` | No | `true` | If `false`, skip `-m` flag on `useradd` (no home dir creation). If `true`, `userdel -r` removes home on delete. |
| `password_max_age` | No | -- | Maximum password age in days. Runs `chage -M <days>` after create/update. |
| `expiredate` | No | -- | Account expiration date (`YYYY-MM-DD`). Passed to `useradd --expiredate` / `usermod --expiredate`. |
| `purge_groups` | No | `false` | When `true` with `groups`, removes user from supplementary groups not in the list (`usermod -G` instead of `-aG`). |
| `sudo_passwd` | No | `false` | If `true`, grant sudo access with password required. Writes `/etc/sudoers.d/<username>`. Mutually exclusive with `sudo_nopasswd`. |
| `sudo_nopasswd` | No | `false` | If `true`, grant passwordless sudo access. Writes `/etc/sudoers.d/<username>` with `NOPASSWD`. Mutually exclusive with `sudo_passwd`. |
| `desktop` | No | -- | Desktop environment for RDP sessions. Writes `~/.xsession` with the appropriate exec command. Values: `cinnamon`, `gnome`, `xfce`, `mate`, `plasma`, `lxde`, `lxqt`, `fvwm`, `i3`, `sway`, `openbox`, `budgie`. Ignored on Windows. |

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

### Expiring contractor account

```yaml
resources:
  - name: contractor
    type: user
    username: contractor
    shell: /bin/bash
    expiredate: "2026-06-30"
    password_max_age: "90"
    groups: "developers"
    purge_groups: "true"
```

### Without home directory

```yaml
resources:
  - name: service-runner
    type: user
    username: svcrun
    manage_home: "false"
    shell: /usr/sbin/nologin
```

### Sudo with password

```yaml
resources:
  - name: deploy user
    type: user
    username: deploy
    shell: /bin/bash
    sudo_passwd: "true"
```

### Passwordless sudo

```yaml
resources:
  - name: admin user
    type: user
    username: admin
    shell: /bin/bash
    sudo_nopasswd: "true"
```


### RDP desktop session

```yaml
resources:
  - name: dan
    type: user
    username: dan
    shell: /bin/bash
    desktop: cinnamon
    authorized_keys: |
      ssh-ed25519 AAAA... dan@workstation
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
- When `state: absent`, the user is deleted with `userdel --remove` (which also removes the home directory) and any `/etc/sudoers.d/<username>` file is removed.
- Sudoers files are written to `/etc/sudoers.d/<username>` with mode `0440`. Setting both `sudo_passwd` and `sudo_nopasswd` to `true` is an error. Removing both params from a user that previously had sudo will delete the sudoers file.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
