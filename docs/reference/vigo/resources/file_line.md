
# file_line

Manages individual lines within a file. Can add, remove, or replace lines based on content or regex matching.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `target_path` | Yes | -- | Absolute path to the file. |
| `line` | No | -- | The line content to ensure is present or absent. |
| `match_pattern` | No | -- | Regex pattern to match existing lines. When combined with `line`, replaces matched lines. |
| `state` | Yes | -- | Desired state: `present` or `absent`. |
| `position` | No | `end` | Where to insert the line: `end`, `begin`, `after:<regex>`, or `before:<regex>`. |
| `region_start` | No | -- | Regex marking the start of a region to constrain operations within. |
| `region_end` | No | -- | Regex marking the end of a region to constrain operations within. |

## States

- **`present`** -- Ensure the line exists in the file. If `match_pattern` is given, replace matching lines with `line`. If no match and line is not found, insert at `position`.
- **`absent`** -- Remove lines matching `line` (exact) or `match_pattern` (regex).

## Idempotency

Reads the file and checks whether the desired line already exists (or matched lines already have the correct content). Only writes when there is a difference.

## Examples

### Basic -- ensure a line exists

```yaml
resources:
  - name: Enable IP forwarding in sysctl.conf
    type: file_line
    target_path: /etc/sysctl.conf
    line: "net.ipv4.ip_forward = 1"
    match_pattern: "^net\\.ipv4\\.ip_forward"
```

### Insert after a pattern

```yaml
resources:
  - name: Add nameserver
    type: file_line
    target_path: /etc/resolv.conf
    line: "nameserver 10.0.0.1"
    position: "after:^search"
```

### Remove a line

```yaml
resources:
  - name: Remove old entry
    type: file_line
    target_path: /etc/hosts
    match_pattern: "old-server\\.example\\.com"
    state: absent
```


### Within a region

```yaml
resources:
  - name: Set option in section
    type: file_line
    target_path: /etc/myapp.conf
    line: "max_connections = 100"
    match_pattern: "^max_connections"
    region_start: "^\\[database\\]"
    region_end: "^\\["
```

## Platform

Cross-platform.

## Notes

- When both `line` and `match_pattern` are provided for `state: present`, lines matching the regex are replaced with the specified `line`.
- The `position` parameter only applies when inserting a new line (no existing match found).
- Region constraints limit operations to lines between `region_start` and `region_end`.

## Choosing the Right File Editor

| Executor | Best for |
|----------|----------|
| **file_line** | Ensuring a single line exists or is absent (e.g., a config directive) |
| [replace](replace.md) | Regex find-and-replace across a file (e.g., updating a version number everywhere) |
| [blockinfile](blockinfile.md) | Managing a cohesive multi-line block with BEGIN/END markers (e.g., SSH hardening section) |
| [file](file.md) | Replacing the entire file content from a template |
| [ini](ini.md) | Managing key=value pairs within INI-style [section] files |
| [json_file](json_file.md) | Setting values in JSON files by dot-path |
| [field_edit](field_edit.md) | Editing fields in columnar files like `/etc/passwd` |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
