
# vigogo webhooks

Manage webhook endpoints for event notifications with HMAC-SHA256 signing.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `add` | Register a new webhook endpoint |
| `list` | List all registered webhooks |
| `delete` | Remove a webhook |

## Usage

```bash
vigogo webhooks [subcommand] [flags]
```

## add

Register a new webhook endpoint for receiving event notifications.

```bash
vigogo webhooks add <url> [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--events` | | `run.failure,drift.detected` | Comma-separated list of event types |
| `--secret` | | | HMAC secret for webhook signing (required) |

### Example

```bash
vigogo webhooks add https://hooks.example.com/vigo \
  --secret "webhook-signing-secret" \
  --events "run.failure,drift.detected,envoy.enrolled"
```

```
Created webhook wh1a2b3c -> https://hooks.example.com/vigo
```

## list

List all registered webhooks.

```bash
vigogo webhooks list
```

### Example output

```
ID          URL                                EVENTS                              ACTIVE
wh1a2b3c    https://hooks.example.com/vigo   run.failure,drift.detected          true
wh4d5e6f    https://slack.example.com/webhook   envoy.enrolled,envoy.revoked      true
```

## delete

Remove a webhook by its ID.

```bash
vigogo webhooks delete <id>
```

### Example

```bash
vigogo webhooks delete wh1a2b3c
```

```
Deleted webhook wh1a2b3c
```

## Related

- [runs](runs.md) -- view run history
- [envoys](envoys.md) -- manage enrolled envoys
