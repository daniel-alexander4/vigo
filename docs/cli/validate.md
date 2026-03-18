# hobnobcli validate

Check `.hnb` files for YAML correctness, structural validity, and idempotency.

## Usage

```bash
hobnobcli validate <file.hnb> [file2.hnb ...] [flags]
```

## Checks Performed

1. **YAML syntax** -- valid YAML, proper indentation, no tabs
2. **Structure** -- required fields present (`name`, resources with `name` + `type`)
3. **Idempotency** -- `exec` resources must have `creates`, `onlyif`, or `unless` guard
4. **Cross-references** -- `depends_on`, `notify`, `subscribes`, `before` must reference existing resource names
5. **Known types** -- unknown resource types blocked unless `skip_validation: true` is set
6. **Redundant triggers** -- both `notify` and `subscribes` on the same pair is an error

For thrall files (those with a top-level `thralls:` key), validates that each entry has a `match` field.

Accepts `.hnb`, `.yaml`, and `.yml` file extensions.

## Examples

### Validate a single module

```bash
hobnobcli validate hnb/proposed/modules/nginx.hnb
```

```
hnb/proposed/modules/nginx.hnb: OK
```

### Validate multiple files

```bash
hobnobcli validate hnb/proposed/modules/*.hnb
```

```
hnb/proposed/modules/nginx.hnb: OK
hnb/proposed/modules/postgres.hnb: OK
hnb/proposed/modules/app.hnb: [error] app: resource "deploy" (exec) missing idempotency guard (add creates, onlyif, or unless)
```

### Exit codes

- `0` -- all checks pass
- `1` -- validation errors found

## Relationship to Config Publish

`hobnobcli validate` checks individual files in isolation. `hobnobcli config publish` runs a more comprehensive pipeline that includes the **module linter** (YAML repair, normalization, auto-commenting with `#~` prefixes) plus full cross-tree validation. Use `validate` for quick spot-checks; use `config publish --dry-run` for the full validation pipeline.

## Related

- [vi](vi.md) -- edit .hnb files with live validation
- [preview](preview.md) -- preview planned changes
- [doctor](doctor.md) -- server health checks including config validation
- [config publish](config.md#config-publish) -- full validation + linting pipeline
