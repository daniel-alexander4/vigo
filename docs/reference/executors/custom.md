# custom

Runs external scripts that implement the Vigo executor protocol via JSON over stdin/stdout. Allows extending the agent with custom resource types.

## Parameters

All parameters from the resource definition are passed to the script. The executor itself does not interpret any specific parameters -- they are forwarded verbatim.

## Protocol

The custom executor invokes the script and sends a JSON object to its stdin:

```json
{
  "params": {
    "name": "my-resource",
    "key": "value",
    ...
  },
  "dry_run": false
}
```

The script must print a JSON response to stdout:

```json
{
  "action": "created",
  "changed": true,
  "error": ""
}
```

### Response Fields

| Field | Type | Description |
|-------|------|-------------|
| `action` | string | One of: `created`, `updated`, `deleted`, `skipped`, or `already in desired state`. |
| `changed` | bool | Whether the system was modified. |
| `error` | string | Error message, or empty string on success. |

## Idempotency

Idempotency is the responsibility of the script. The agent calls the script on every convergence -- the script must check current state and decide whether to act.

## Examples

### Script definition

Place scripts in `/etc/vigo/executors/` (or the configured custom executor directory). The filename (without extension) becomes the executor type name.

`/etc/vigo/executors/myresource`:

```bash
#!/bin/bash
INPUT=$(cat)
DRY_RUN=$(echo "$INPUT" | jq -r '.dry_run')
NAME=$(echo "$INPUT" | jq -r '.params.name')

# Check current state
if [ -f "/var/lib/myapp/$NAME" ]; then
    echo '{"action": "already in desired state", "changed": false, "error": ""}'
    exit 0
fi

if [ "$DRY_RUN" = "true" ]; then
    echo '{"action": "created", "changed": true, "error": ""}'
    exit 0
fi

# Apply change
touch "/var/lib/myapp/$NAME"
echo '{"action": "created", "changed": true, "error": ""}'
```

### Using the custom executor

```yaml
resources:
  - name: my-custom-thing
    type: myresource
    key: value
    another_param: something
```

## Platform

Cross-platform. Scripts must be executable and available at the configured path.

## Notes

- Custom executors cannot override built-in executor type names.
- The script must be executable (`chmod +x`).
- The script receives all resource parameters in the `params` object.
- Errors from the script (non-zero exit, invalid JSON, or a non-empty `error` field) are reported as resource failures.
- Scripts should respect the `dry_run` flag and avoid mutating system state when it is `true`.
