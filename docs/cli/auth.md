# hobnobcli auth

Manage CLI authentication tokens for the REST API.

## Subcommands

### set-token

Store an API token for CLI use. The token is saved to `~/.config/hobnob/token` (mode 0600).

```bash
hobnobcli auth set-token hbt_abc123...
```

### status

Show the currently stored token and its validity.

```bash
hobnobcli auth status
```

### clear

Remove the stored API token.

```bash
hobnobcli auth clear
```
