
# vigocli tokens

Manage enrollment tokens used for agent bootstrap.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `generate` | Generate a new one-time enrollment token |

## Usage

```bash
vigocli tokens [subcommand] [flags]
```

## generate

Generate a new one-time enrollment token and print the corresponding bootstrap command.

```bash
vigocli tokens generate
```

### Example

```bash
vigocli tokens generate
```

```
Token: abc123def456

Bootstrap command:
  curl -fsSLk https://localhost:8443/bootstrap | VIGO_TOKEN=abc123def456 bash
```

## Related

- [approved](approved.md) -- manage approved enrollment patterns
- [envoys](envoys.md) -- manage enrolled envoys
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/license.md).*
