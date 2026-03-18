# hobnobcli webusers

Manage web UI and API user accounts.

## Subcommands

### list

List all user accounts.

```bash
hobnobcli webusers list
```

```
ID                                    USERNAME    DISPLAY NAME    EMAIL                 ROLE
a1b2c3d4-e5f6-4a7b-8c9d-e0f1a2b3c4d5  admin       Administrator                         admin
f6e5d4c3-b2a1-4f6e-9d8c-7b6a5f4e3d2c  alice       Alice Smith     alice@example.com      viewer
```

### create

Create a new user account. The first user created is always promoted to admin.

```bash
# Create an admin account
hobnobcli webusers create --username admin --role admin

# Create a viewer with details
hobnobcli webusers create --username alice --role viewer \
  --display-name "Alice Smith" --email alice@example.com

# Create a user with an SSH key (for isowebauth)
hobnobcli webusers create --username bob --role viewer \
  --ssh-key-file ~/.ssh/id_ed25519.pub
```

| Flag | Default | Description |
|------|---------|-------------|
| `--username` | — | Username (required) |
| `--role` | `viewer` | Role: `admin` or `viewer` |
| `--display-name` | — | Display name |
| `--email` | — | Email address |
| `--ssh-key-file` | — | Path to SSH public key file (isowebauth) |

### Setting a password

Web UI passwords are managed via the secrets system:

```bash
hobnobcli secrets set-web-password hobnob/web/auth/admin
```

See [hobnobcli secrets](secrets.md) for details.

### delete

Permanently remove a user account.

```bash
hobnobcli webusers delete --username alice
```

| Flag | Default | Description |
|------|---------|-------------|
| `--username` | — | Username (required) |
