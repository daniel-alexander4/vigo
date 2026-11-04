
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

Generate a new one-time enrollment token and print the two commands that use it. The server does not store the token until you bind it to a hostname glob with [`vigocli approved add`](approved.md); until then an envoy presenting it is refused.

```bash
vigocli tokens generate
```

### Example

```bash
vigocli tokens generate
```

```
Token: abc123def456

1. Bind it to the hostnames allowed to use it:
  vigocli approved add '<hostname-glob>' --token abc123def456

2. Run on the new envoy:
  curl -sSfk "https://<server-address>:8443/bootstrap?token=abc123def456" | sudo sh

Replace <server-address> with an address the new envoy can reach this server on.
```

Run with the default `--server` (a loopback address), vigocli can't know the
address an envoy reaches the server on, so it prints `<server-address>`. Pass
`--server https://vigo.example.com:8443` to get a ready-to-paste command.

## Related

- [approved](approved.md) -- manage approved enrollment patterns
- [envoys](envoys.md) -- manage enrolled envoys
