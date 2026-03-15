# hobnobcli auth

Manage CLI authentication tokens for the REST API.

## Localhost Trust

When the CLI runs on the same host as the server (or the Docker host), all API requests are automatically trusted with admin privileges. No token is needed. This works like PostgreSQL's `peer` authentication — shell access to the server host implies full access.

Trusted source addresses: `127.0.0.1`, `::1`, and Docker bridge networks (`172.16.0.0/12`).

## Remote Access

When pointing the CLI at a remote server (`--server http://remote:8181`), an API token is required:

```bash
# Generate a token from the web UI admin panel or via local CLI
hobnobcli auth set-token hbt_abc123...
```

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
