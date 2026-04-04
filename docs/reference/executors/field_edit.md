
# field_edit

Edits individual fields within columnar or tabular files like `/etc/passwd`, `/etc/group`, or CSV-style configs. Targets a specific line by regex, then modifies a specific field by position.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `path` | Yes | -- | Absolute path to the file. |
| `line_match` | Yes | -- | Regex to identify the target line. |
| `select_field` | Yes | -- | 1-indexed field number to modify. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `field_separator` | No | `:` | Character used to split fields. |
| `field_value` | Yes (for present) | -- | The value to set on the selected field. |
| `field_operation` | No | `set` | Operation to perform: `set`, `append`, `prepend`, or `delete`. |
| `value_separator` | No | `,` | Separator for multi-value fields (used by append/prepend/delete operations). |
| `extend_fields` | No | `false` | If `true`, extend the line with empty fields if `select_field` exceeds the current field count. |

## States

- **`present`** -- Ensure the specified field has the desired value.
- **`absent`** -- Remove the value from the specified field (for multi-value fields) or clear the field.

## Idempotency

Reads the file, finds the matching line, extracts the target field, and compares it against the desired value and operation. Only writes when the field content would change.

## Examples

### Set a user's shell in /etc/passwd

```yaml
resources:
  - name: Set deploy user shell
    type: field_edit
    path: /etc/passwd
    line_match: "^deploy:"
    select_field: "7"
    field_separator: ":"
    field_value: /bin/bash
```

### Append a user to a group in /etc/group

```yaml
resources:
  - name: Add deploy to docker group
    type: field_edit
    path: /etc/group
    line_match: "^docker:"
    select_field: "4"
    field_separator: ":"
    field_value: deploy
    field_operation: append
    value_separator: ","
```

### Remove a user from a group


```yaml
resources:
  - name: Remove olduser from docker group
    type: field_edit
    path: /etc/group
    line_match: "^docker:"
    select_field: "4"
    field_separator: ":"
    field_value: olduser
    field_operation: delete
    value_separator: ","
```

## Platform

Cross-platform.

## Notes

- Field numbering is 1-based (field 1 is the first field).
- The `extend_fields` parameter handles cases where the line has fewer fields than `select_field` by padding with empty fields.
- Operations `append`/`prepend`/`delete` work on multi-value fields separated by `value_separator`.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
