
# vigocli webusers

Manage web UI and API user accounts.

## Subcommands

### list

List all user accounts.

```bash
vigocli webusers list
```

```
ID                                    USERNAME    DISPLAY NAME    EMAIL                 ROLE
a1b2c3d4-e5f6-4a7b-8c9d-e0f1a2b3c4d5  admin       Administrator                         admin
f6e5d4c3-b2a1-4f6e-9d8c-7b6a5f4e3d2c  alice       Alice Smith     alice@example.com      viewer
```

### create

Create a new user account. **Admin role** is constrained: the username must be a real human OS account on the server host (uid≥1000, real shell), validated against the local agent's `users.human` trait. The pubkey is auto-read from the matching OS user's `~/.ssh/id_ed25519.pub` (fallback `id_rsa.pub`); `--ssh-key-file` is rejected. **Viewer / compliance** roles are unconstrained one-offs — no OS-user requirement, `--ssh-key-file` accepted.

The username `localhost` is reserved (it is the identifier of the server-host local-admin principal) and is rejected for any role.

```bash
# Per-operator admin — run from the server host as root. Username must
# match a real OS account; pubkey is auto-read from that account's home.
sudo vigocli webusers create --username dan --role admin

# Viewer with details (no OS-user constraint):
sudo vigocli webusers create --username alice --role viewer \
  --display-name "Alice Smith" --email alice@example.com

# Viewer with explicit SSH key (for isowebauth):
sudo vigocli webusers create --username bob --role viewer \
  --ssh-key-file ~/.ssh/id_ed25519.pub

# Compliance-staff account (compliance.read + compliance.docs.write only):
sudo vigocli webusers create --username carol --role compliance \
  --display-name "Carol Compliance" --email carol@example.com
```

| Flag | Default | Description |
|------|---------|-------------|
| `--username` | — | Username (required). For `--role admin`, must match a human OS user on the server host. `localhost` is reserved and rejected. |
| `--role` | `viewer` | Role: `admin`, `viewer`, or `compliance`. `admin` requires OS-user mapping; others are one-offs. |
| `--display-name` | — | Display name |
| `--email` | — | Email address |
| `--ssh-key-file` | — | Path to SSH public key file. **Rejected for `--role admin`** — admins always auto-read from the matching OS user's home dir. Optional for viewer/compliance (used for isowebauth). |

To re-read an admin user's pubkey after key rotation, delete and re-create the account:

```bash
sudo vigocli webusers delete --username dan
sudo vigocli webusers create --username dan --role admin
```

The recreate auto-reads the (new) pubkey from `/home/<username>/.ssh/id_*.pub`. Sign out and back in on the browser side so the session cache picks up the rotated key.

### Setting a password

Web UI passwords are managed via the secrets system, keyed per username:

```bash
vigocli secrets set password vigo/web/auth/dan
```

See [vigocli secrets](secrets.md) for details.

### set-role

Change an existing user's role. Permissions are recalculated from the new role and the change is written to the audit log.

```bash
vigocli webusers set-role --username alice --role compliance

# Promoting to admin — run from the server host. The username must match
# a human OS account (same constraint as `create --role admin`), and the
# command auto-reads that OS user's ~/.ssh/id_ed25519.pub (fallback
# id_rsa.pub) into the user's DB-side SSH pubkey.
sudo vigocli webusers set-role --username dan --role admin
```

For a demotion or a switch between `viewer` and `compliance`, only the role changes — display name, email, and the SSH public key are preserved. Demoting an operator out of `admin` takes effect at their next login; sessions already open keep their elevated permissions until then.

For `--role admin` the command also **seeds (or re-seeds) the SSH public key** from the matching OS user's standard key location — admin webusers must carry one to back browser-scrier landing and audit attribution. Re-running `set-role --role admin` on a user who is *already* admin is therefore not a no-op: it refreshes the stored pubkey, which is how you update Vigo after rotating that OS user's SSH key.

| Flag | Default | Description |
|------|---------|-------------|
| `--username` | — | Username (required) |
| `--role` | — | New role: `admin`, `viewer`, or `compliance` (required). `admin` requires a matching human OS user on the server host with an `~/.ssh/id_ed25519.pub` (or `id_rsa.pub`); the pubkey is auto-read. |

### delete

Permanently remove a user account.

```bash
vigocli webusers delete --username alice
```

| Flag | Default | Description |
|------|---------|-------------|
| `--username` | — | Username (required) |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
