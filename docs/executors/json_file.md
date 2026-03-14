# json_file

Manages individual keys in JSON files using dot-separated key paths.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `path` | Yes | -- | Absolute path to the JSON file. |
| `key` | Yes | -- | Dot-separated key path (e.g., `database.host` or `server.ports.0`). |
| `value` | Yes (for present) | -- | The value to set, as a JSON string (e.g., `"\"localhost\""`, `"8080"`, `"true"`, `"[1,2,3]"`). |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `indent` | No | `  ` (2 spaces) | Indentation string for pretty-printing the output. |

## States

- **`present`** -- Ensure the key exists with the specified value.
- **`absent`** -- Remove the key from the JSON object.

## Idempotency

Reads and parses the JSON file, navigates to the specified key path, and compares the current value against the desired value. Only writes when there is a difference.

## Examples

### Set a string value

```yaml
resources:
  - name: Set database host
    type: json_file
    path: /etc/myapp/config.json
    key: database.host
    value: '"db.internal"'
```

### Set a numeric value

```yaml
resources:
  - name: Set server port
    type: json_file
    path: /etc/myapp/config.json
    key: server.port
    value: "8080"
```

### Set a boolean

```yaml
resources:
  - name: Enable debug mode
    type: json_file
    path: /etc/myapp/config.json
    key: debug
    value: "true"
```

### Remove a key

```yaml
resources:
  - name: Remove deprecated setting
    type: json_file
    path: /etc/myapp/config.json
    key: legacy.feature_flag
    state: absent
```

## Platform

Cross-platform.

## Notes

- The `value` parameter must be a valid JSON value. Strings must be quoted (e.g., `'"hello"'`), numbers and booleans are bare.
- Nested keys are created automatically. Setting `database.host` will create the `database` object if it does not exist.
- Array indices can be used in key paths (e.g., `servers.0.host`).
- The file is pretty-printed with the configured indentation after modification.
