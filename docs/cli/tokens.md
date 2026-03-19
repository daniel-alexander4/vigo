# vigogo tokens

Manage enrollment tokens used for agent bootstrap.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `generate` | Generate a new one-time enrollment token |

## Usage

```bash
vigogo tokens [subcommand] [flags]
```

## generate

Generate a new one-time enrollment token and print the corresponding bootstrap command.

```bash
vigogo tokens generate
```

### Example

```bash
vigogo tokens generate
```

```
Token: abc123def456

Bootstrap command:
  curl -fsSLk https://localhost:8443/bootstrap | VIGO_TOKEN=abc123def456 bash
```

## Related

- [approved](approved.md) -- manage approved enrollment patterns
- [envoys](envoys.md) -- manage enrolled envoys
