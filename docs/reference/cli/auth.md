
# vigogo auth

Manage CLI authentication tokens for the REST API.

## Localhost Trust

When the CLI runs on the same host as the server (or the Docker host), all API requests are automatically trusted with admin privileges. No token is needed. This works like PostgreSQL's `peer` authentication — shell access to the server host implies full access.

Trusted source addresses: `127.0.0.1`, `::1`, and Docker bridge networks (`172.16.0.0/12`).

## Remote Access

When pointing the CLI at a remote server (`--server http://remote:8443`), an API token is required:

```bash
# Generate a token from the web UI admin panel or via local CLI
vigogo auth set-token vgot_abc123...
```

## Subcommands

### set-token

Store an API token for CLI use. The token is saved to `~/.config/vigo/token` (mode 0600).

```bash
vigogo auth set-token vgot_abc123...
```

### status

Show the currently stored token and its validity.

```bash
vigogo auth status
```

### clear

Remove the stored API token.

```bash
vigogo auth clear
```

### create-token

Create a new API token on the server. The plaintext token is shown once and cannot be retrieved again.

```bash
vigogo auth create-token --name "ci-automation" --expires-in "720h"
```

Output:
```
Token created.
  ID:    tok_a1b2c3d4
  Name:  ci-automation
  Token: vgot_abc123...

Save this token — it will not be shown again.
To use it: vigogo auth set-token vgot_abc123...
```

**Flags:**

| Flag | Description |
|------|-------------|
| `--name` | Descriptive name for the token |
| `--expires-in` | Expiry duration (e.g., `720h` for 30 days). Omit for no expiry. |

### list-tokens

List your API tokens on the server. Shows ID, name, prefix, creation date, and expiry.

```bash
vigogo auth list-tokens
```

Output:
```
ID              NAME            PREFIX    CREATED     EXPIRES
tok_a1b2c3d4    ci-automation   vgot_abc   2026-03-16  2026-04-15
tok_e5f6a7b8    monitoring      vgot_def   2026-03-10  never
```

### revoke-token

Revoke an API token by ID. The token is immediately invalidated.

```bash
vigogo auth revoke-token tok_a1b2c3d4
```

### emergency-access

Record an emergency access event in the tamper-evident audit trail (HIPAA 164.312(a)(2)(ii)). Use this when invoking break-glass procedures — e.g., during an IdP outage, account lockout, or incident response.

```bash
vigogo auth emergency-access --reason "OIDC provider outage, reviewing compliance status"
```

The event is recorded as `emergency.access` in the SHA-256 hash chain and cannot be retroactively modified.

**Flags:**

| Flag | Description |
|------|-------------|
| `--reason` | Required. The reason for emergency access. |

See [Emergency Access Procedure](../../compliance/emergency-access.md) for the full procedure.
