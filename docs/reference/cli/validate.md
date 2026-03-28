
# vigogo validate

Check `.vgo` files for YAML correctness, structural validity, and idempotency.

## Usage

```bash
vigogo validate <file.vgo> [file2.vgo ...] [flags]
```

## Checks Performed

1. **YAML syntax** -- valid YAML, proper indentation, no tabs
2. **Structure** -- required fields present (`name`, resources with `name` + `type`)
3. **Idempotency** -- `exec` resources must have `creates`, `onlyif`, or `unless` guard
4. **Cross-references** -- `depends_on`, `notify`, `subscribes`, `before` must reference existing resource names
5. **Known types** -- unknown resource types blocked unless `skip_validation: true` is set
6. **Redundant triggers** -- both `notify` and `subscribes` on the same pair is an error

For envoy files (those with a top-level `envoys:` key), validates that each entry has a `match` field.

Accepts `.vgo`, `.yaml`, and `.yml` file extensions.

## Examples

### Validate a single module

```bash
vigogo validate stack/stage/modules/nginx.vgo
```

```
stack/stage/modules/nginx.vgo: OK
```

### Validate multiple files

```bash
vigogo validate stack/stage/modules/*.vgo
```

```
stack/stage/modules/nginx.vgo: OK
stack/stage/modules/postgres.vgo: OK
stack/stage/modules/app.vgo: [error] app: resource "deploy" (exec) missing idempotency guard (add creates, onlyif, or unless)
```

### Exit codes

- `0` -- all checks pass
- `1` -- validation errors found

## Relationship to Config Publish

`vigogo validate` checks individual files in isolation. `vigogo config publish` runs a more comprehensive pipeline that includes the **module linter** (YAML repair, normalization, auto-commenting with `#~` prefixes) plus full cross-tree validation. Use `validate` for quick spot-checks; use `config publish --dry-run` for the full validation pipeline.

## Related

- [vi](vi.md) -- edit .vgo files with live validation
- [preview](preview.md) -- preview planned changes
- [doctor](doctor.md) -- server health checks including config validation
- [config publish](config.md#config-publish) -- full validation + linting pipeline
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
