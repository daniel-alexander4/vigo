# replace

Performs regex find-and-replace operations within a file. Supports backreferences and region constraints.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `path` | Yes | -- | Absolute path to the file. |
| `match` | Yes | -- | Regex pattern to search for. |
| `replace` | Yes | -- | Replacement string. Supports backreferences (`$1`, `$2`, etc.). |
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
    path: /etc/nginx/sites-available/default
    match: "listen \\d+"
    replace: "listen 8080"
```

### With backreferences

```yaml
resources:
  - name: Comment out debug settings
    type: replace
    path: /etc/myapp.conf
    match: "^(debug_level\\s*=\\s*\\d+)"
    replace: "# $1"
```

### Replace first occurrence only

```yaml
resources:
  - name: Update first server entry
    type: replace
    path: /etc/haproxy/haproxy.cfg
    match: "server old-host"
    replace: "server new-host"
    occurrences: first
```

### Within a region

```yaml
resources:
  - name: Update database port in production section
    type: replace
    path: /etc/myapp.conf
    match: "port = \\d+"
    replace: "port = 5433"
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
