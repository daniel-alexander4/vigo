# vigogo env

Manage environment tier operations for promoting variables between environments.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `promote` | Promote environment variables from one tier to another |

## Usage

```bash
vigogo env [subcommand] [flags]
```

## promote

Show the variable differences between two environment tiers for a module. This is an informational command -- config files are not modified automatically.

```bash
vigogo env promote <module> --from <env> --to <env>
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--from` | | | Source environment tier (required) |
| `--to` | | | Target environment tier (required) |

### Example

```bash
vigogo env promote nginx --from staging --to production
```

```
Promotion p1a2b3c4: staging -> production

Variable changes:
  worker_connections: 1024 -> 2048
  log_level: debug -> warn

Config files not modified. Edit YAML files manually to apply.
```

## Related

- [preview](preview.md) -- preview planned changes
- [validate](validate.md) -- validate .vgo configuration files
