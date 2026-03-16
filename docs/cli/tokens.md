# hobnobcli tokens

Manage enrollment tokens used for agent bootstrap.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `generate` | Generate a new one-time enrollment token |

## Usage

```bash
hobnobcli tokens [subcommand] [flags]
```

## generate

Generate a new one-time enrollment token and print the corresponding bootstrap command.

```bash
hobnobcli tokens generate
```

### Example

```bash
hobnobcli tokens generate
```

```
Token: abc123def456

Bootstrap command:
  curl -fsSL https://localhost:8443/bootstrap | HOBNOB_TOKEN=abc123def456 bash
```

## Related

- [approved](approved.md) -- manage approved enrollment patterns
- [thralls](thralls.md) -- manage enrolled thralls
