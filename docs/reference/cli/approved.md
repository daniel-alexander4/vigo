
# vigogo approved

Manage approved enrollment patterns that control which hostnames can enroll as envoys.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `add` | Add an approved pattern with a one-time token |
| `list` | List all approved patterns |
| `revoke` | Revoke an approved pattern |

## Usage

```bash
vigogo approved [subcommand] [flags]
```

## add

Add an approved hostname pattern with a one-time enrollment token.

```bash
vigogo approved add <pattern> --token <token>
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--token` | | | One-time enrollment token (required) |

### Examples

#### Add a wildcard pattern for web servers

```bash
vigogo approved add "*.web.example.com" --token "my-secret-token"
```

```
Created approved pattern *.web.example.com (ID: a1b2c3d4)
```

## list

List all approved patterns with their usage status.

```bash
vigogo approved list
```

### Example output

```
ID          PATTERN               CREATED           USED
a1b2c3d4    *.web.example.com     2026-03-10 14:30  no
e5f6g7h8    db-*.internal.net     2026-03-08 09:15  yes (db-01.internal.net)
```

## revoke

Revoke an approved pattern by its ID.

```bash
vigogo approved revoke <id>
```

### Example

```bash
vigogo approved revoke a1b2c3d4
```

```
Revoked approved pattern a1b2c3d4
```

## Related

- [tokens](tokens.md) -- generate enrollment tokens
- [envoys](envoys.md) -- manage enrolled envoys
