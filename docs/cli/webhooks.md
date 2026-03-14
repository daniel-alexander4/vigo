# hobnobcli webhooks

Manage webhook endpoints for event notifications with HMAC-SHA256 signing.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `add` | Register a new webhook endpoint |
| `list` | List all registered webhooks |
| `delete` | Remove a webhook |

## Usage

```bash
hobnobcli webhooks [subcommand] [flags]
```

## add

Register a new webhook endpoint for receiving event notifications.

```bash
hobnobcli webhooks add <url> [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--events` | | `run.failure,drift.detected` | Comma-separated list of event types |
| `--secret` | | | HMAC secret for webhook signing (required) |

### Example

```bash
hobnobcli webhooks add https://hooks.example.com/hobnob \
  --secret "webhook-signing-secret" \
  --events "run.failure,drift.detected,thrall.enrolled"
```

```
Created webhook wh1a2b3c -> https://hooks.example.com/hobnob
```

## list

List all registered webhooks.

```bash
hobnobcli webhooks list
```

### Example output

```
ID          URL                                EVENTS                              ACTIVE
wh1a2b3c    https://hooks.example.com/hobnob   run.failure,drift.detected          true
wh4d5e6f    https://slack.example.com/webhook   thrall.enrolled,thrall.revoked      true
```

## delete

Remove a webhook by its ID.

```bash
hobnobcli webhooks delete <id>
```

### Example

```bash
hobnobcli webhooks delete wh1a2b3c
```

```
Deleted webhook wh1a2b3c
```

## Related

- [runs](runs.md) -- view run history
- [thralls](thralls.md) -- manage enrolled thralls
