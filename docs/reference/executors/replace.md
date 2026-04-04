
# replace

Performs regex find-and-replace operations within a file. Supports backreferences and region constraints.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `target_path` | Yes | -- | Absolute path to the file. |
| `match_pattern` | Yes | -- | Regex pattern to search for. |
| `replacement` | Yes | -- | Replacement string. Supports backreferences (`$1`, `$2`, etc.). |
| `occurrences` | No | `all` | How many matches to replace: `all` or `first`. |
| `region_start` | No | -- | Regex marking the start of a region to constrain replacements within. |
| `region_end` | No | -- | Regex marking the end of a region to constrain replacements within. |

## States

The replace executor does not use a `state` parameter. It always operates in "present" mode -- finding and replacing matches.

## Idempotency

Reads the file, applies the regex replacement, and compares the result to the original. If the content is unchanged (no matches or replacement produces the same text), no write occurs and `Action::None` is returned.

## Examples

### Basic

```yaml
resources:
  - name: Update listen port
    type: replace
    target_path: /etc/nginx/sites-available/default
    match_pattern: "listen \\d+"
    replacement: "listen 8080"
```

### With backreferences

```yaml
resources:
  - name: Comment out debug settings
    type: replace
    target_path: /etc/myapp.conf
    match_pattern: "^(debug_level\\s*=\\s*\\d+)"
    replacement: "# $1"
```

### Replace first occurrence only

```yaml
resources:
  - name: Update first server entry
    type: replace
    target_path: /etc/haproxy/haproxy.cfg
    match_pattern: "server old-host"
    replacement: "server new-host"
    occurrences: first
```


### Within a region

```yaml
resources:
  - name: Update database port in production section
    type: replace
    target_path: /etc/myapp.conf
    match_pattern: "port = \\d+"
    replacement: "port = 5433"
    region_start: "^\\[production\\]"
    region_end: "^\\["
```

## Platform

Cross-platform.

## Notes

- Uses Rust regex syntax. Backreferences use `$1`, `$2` notation.
- Region constraints limit replacements to lines between `region_start` and `region_end` markers.
- If no matches are found, the executor returns `Action::None`.

## Choosing the Right File Editor

| Executor | Best for |
|----------|----------|
| **replace** | Regex find-and-replace across a file (e.g., updating a version or IP everywhere) |
| [file_line](file_line.md) | Ensuring a single line exists, without regex complexity |
| [blockinfile](blockinfile.md) | Managing a multi-line block with markers, not individual patterns |
| [file](file.md) | Replacing the entire file content from a template |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
