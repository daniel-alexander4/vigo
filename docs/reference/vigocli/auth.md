
# vigocli auth

Manage CLI authentication tokens for the REST API.

## On the server host

When the CLI runs on the server host it authenticates with the local-admin token the server mints on first boot at `/srv/vigo/cli-admin-token` (mode `0600`, root-owned), so `sudo vigocli ...` needs no setup. The source address grants nothing: a request without a valid token is refused wherever it comes from.

## Remote Access

When pointing the CLI at a remote server (`--server http://remote:8443`), an API token is required:

```bash
# Generate a token from the web UI admin panel or via local CLI, then:
vigocli auth set-token        # prompts for the token without echo
```

## Subcommands

### set-token

Store an API token for CLI use. The token is saved to `~/.config/vigo/token` (mode 0600).

With no argument it prompts for the token without echo, or reads the first line of stdin when stdin is a pipe. A token passed as an argument works too, but it lands in shell history and the process list.

```bash
vigocli auth set-token                 # prompt
vigocli auth set-token < token.txt     # from a file or pipe
```

### status

Show the currently stored token and its validity.

```bash
vigocli auth status
```

### clear

Remove the stored API token.

```bash
vigocli auth clear
```

### create-token

Create a new API token on the server. The plaintext token is shown once and cannot be retrieved again. The token belongs to a web user and inherits that user's role and permissions when used.

```bash
vigocli auth create-token --name "ci-automation" --expires-in "720h"
```

Output:
```
Token created.
  ID:    tok_a1b2c3d4
  Name:  ci-automation
  Token: vgot_abc123...
Save this token — it will not be shown again.
To use it: vigocli auth set-token   (paste the token at the prompt)
```

**Token ownership.** Normally the token belongs to the web user you are authenticated as. When you run `vigocli` **on the server host**, the CLI authenticates as the local-admin principal (the boot-minted root token), which is not a web user — so you must name the owner explicitly with `--user`:

```bash
vigocli auth create-token --name "ci-automation" --user dan
```

Omitting `--user` on the server host fails with `token owner must be a real web user`. `--user` is honored only for the local-admin principal; a token authenticated as a regular web user cannot mint tokens for anyone else.

**Flags:**

| Flag | Description |
|------|-------------|
| `--name` | Descriptive name for the token |
| `--expires-in` | Expiry duration (e.g., `720h` for 30 days). Omit for no expiry. |
| `--user` | Owner web user (e.g. `dan`). Required when running on the server host. |

### list-tokens

List your API tokens on the server. Shows ID, name, prefix, creation date, and expiry.

```bash
vigocli auth list-tokens
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
vigocli auth revoke-token tok_a1b2c3d4
```

### emergency-access

Record an emergency access event in the tamper-evident audit trail (HIPAA 164.312(a)(2)(ii)). Use this when invoking break-glass procedures — e.g., during an IdP outage, account lockout, or incident response.

```bash
vigocli auth emergency-access --reason "OIDC provider outage, reviewing compliance status"
```

The event is recorded as `emergency.access` in the SHA-256 hash chain and cannot be retroactively modified.

**Flags:**

| Flag | Description |
|------|-------------|
| `--reason` | Required. The reason for emergency access. |

See [Emergency Access Procedure](../compliance-matrix.md) for the full procedure.
