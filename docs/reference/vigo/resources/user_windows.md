
# user_windows

Manages local Windows user accounts via `net user` and `net localgroup`.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `username` | Yes | -- | Username. |
| `state` | Yes | -- | Desired state: `present` or `absent`. |
| `password` | No | -- | User password (for account creation). |
| `comment` | No | -- | User description/comment field. |
| `groups` | No | -- | Comma-separated list of local groups to add the user to. |

## States

- **`present`** -- Ensure the user exists. Creates with `net user <name> /add` if missing. Checks and updates group memberships.
- **`absent`** -- Delete the user with `net user <name> /delete`.

## Idempotency

Checks if the user exists via `net user <name>`. For existing users, parses the "Local Group Memberships" output to determine which groups need adding. Only creates or modifies when needed.

## Examples

### Basic

```yaml
resources:
  - name: Deploy user
    type: user
    username: deploy
    groups: "Administrators,Remote Desktop Users"
```

### With password

```yaml
resources:
  - name: Service account
    type: user
    username: svc-myapp
    password: "secret:windows/svc-myapp-password"
    comment: "MyApp service account"
```

### Remove a user

```yaml
resources:
  - name: Old user
    type: user
    username: old-user
    state: absent
```

## Platform

Windows only. On Linux, `type: user` maps to the [user](user.md) executor.

## Notes

- Uses `net user` for account creation/deletion and `net localgroup` for group membership.
- Group membership comparison is case-insensitive.
- When creating a user with a password, the password is passed as a positional argument to `net user`.
