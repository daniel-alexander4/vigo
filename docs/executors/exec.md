# exec

Runs arbitrary commands with optional guard conditions. Supports piping secrets via stdin or temporary files.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `command` | Yes | -- | The command to execute (passed to `sh -c`). |
| `creates` | No | -- | Skip if this file path already exists. |
| `onlyif` | No | -- | Run the command only if this guard command succeeds (exit 0). |
| `unless` | No | -- | Run the command only if this guard command fails (non-zero exit). |
| `stdin` | No | -- | Data to pipe to the command's stdin. Mutually exclusive with `secret_file`. |
| `secret_file` | No | -- | Secret value written to a temporary file (mode 0600). The placeholder `{{secret_file}}` in `command` is replaced with the temp file path. Mutually exclusive with `stdin`. |
| `cwd` | No | -- | Working directory for command execution. |
| `user` | No | -- | Run command as a different user (Unix only, via `su`). |
| `environment` | No | -- | Comma-separated `KEY=VALUE` environment variables. |
| `returns` | No | `0` | Comma-separated acceptable exit codes (e.g., `0,1,2`). |
| `refreshonly` | No | `false` | Only execute if triggered by `notify`/`subscribes`. |
| `logoutput` | No | `true` | When `false`, suppress stdout/stderr in error messages. |

## States

The exec executor does not use a `state` parameter. It either runs or is skipped based on guards.

## Idempotency

Idempotency is achieved through guard conditions:

- **`creates`** -- If the specified file exists, the command is skipped (`Action::Skipped`).
- **`onlyif`** -- The guard command is run first. If it exits non-zero, the main command is skipped.
- **`unless`** -- The guard command is run first. If it exits zero, the main command is skipped.

Without guards, the command runs on every convergence.

## Examples

### Basic

```yaml
resources:
  - name: Initialize database
    type: exec
    command: /opt/myapp/bin/init-db
    creates: /var/lib/myapp/db.sqlite
```

### With onlyif guard

```yaml
resources:
  - name: Apply migrations
    type: exec
    command: /opt/myapp/bin/migrate
    onlyif: /opt/myapp/bin/check-pending-migrations
```

### With unless guard

```yaml
resources:
  - name: Compile assets
    type: exec
    command: npm run build
    unless: test -f /opt/myapp/dist/index.js
```

### With stdin

```yaml
resources:
  - name: Set Grafana admin password
    type: exec
    command: grafana-cli admin reset-admin-password --password-from-stdin
    stdin: "secret:grafana/admin-password"
```

### With secret_file

```yaml
resources:
  - name: Import TLS certificate
    type: exec
    command: certutil -import -f {{secret_file}}
    secret_file: "secret:myapp/tls-cert"
```

### With depends_on

```yaml
resources:
  - name: Install myapp
    type: package
    package: myapp

  - name: Initialize myapp
    type: exec
    command: /opt/myapp/bin/init
    creates: /var/lib/myapp/initialized
    depends_on: Install myapp
```

## Platform

Cross-platform. Commands are executed via `sh -c` on Unix and `cmd /C` on Windows.

## Notes

- The `stdin` and `secret_file` parameters are mutually exclusive. Using both is an error.
- `secret_file` creates a temporary file with mode 0600, writes the secret, replaces `{{secret_file}}` in the command, runs the command, then deletes the file. The secret never appears in command arguments or logs.
- If no guard is provided, the command always runs (always returns `changed: true`).
