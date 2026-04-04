
# exec

Runs arbitrary commands with optional guard conditions. Supports piping secrets via stdin or temporary files.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `command` | Yes | -- | The command to execute (passed to `sh -c` by default). |
| `onlyif` | No | -- | Run the command only if this guard command succeeds (exit 0). |
| `unless` | No | -- | Run the command only if this guard command fails (non-zero exit). |
| `stdin` | No | -- | Data to pipe to the command's stdin. Mutually exclusive with `secret_file`. |
| `secret_file` | No | -- | Secret value written to a temporary file (mode 0600). The placeholder `{{secret_file}}` in `command` is replaced with the temp file path. Mutually exclusive with `stdin`. |
| `cwd` | No | -- | Working directory for command execution. |
| `user` | No | -- | Run command as a different user (Unix only, via `su`). |
| `environment` | No | -- | Comma-separated `KEY=VALUE` environment variables. |
| `returns` | No | `0` | Comma-separated acceptable exit codes (e.g., `0,1,2`). |
| `logoutput` | No | `true` | When `false`, suppress stdout/stderr in error messages. |
| `background` | No | `false` | When `true`, spawn the command and return immediately without waiting for completion. Guards still run synchronously. No output capture or exit code checking. Background commands auto-nice to 19 unless overridden by `nice`. |
| `nice` | No | `0` | CPU/IO priority level 0–19 (0 = normal, 19 = lowest). Background execs default to 19. On Linux, also sets I/O priority proportionally (best-effort class). |
| `timeout` | No | `300` | Execution timeout in seconds. The agent kills the command if it exceeds this duration. |
| `retries` | No | `0` | Number of retry attempts on failure. Only the command is retried — guards are not re-evaluated. Background execs are not retried. |
| `retry_delay` | No | `5` | Seconds to wait between retry attempts. |
| `sensitive` | No | `false` | When `true`, suppress the command string from result names, logs, and error messages. Distinct from `logoutput: false` which only suppresses stdout/stderr. |
| `shell` | No | `sh` | Shell to use for command execution (e.g., `bash`, `zsh`, `fish`). Default is `sh` on Unix, `cmd` on Windows. Guards also use the specified shell. |

## States

The exec executor does not use a `state` parameter. It either runs or is skipped based on guards.

## Idempotency

Idempotency is achieved through guard conditions:

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
    unless: "test -f /var/lib/myapp/db.sqlite"
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
    watch_secret: ["grafana/admin-password"]
    when: "changed"
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
    unless: "test -f /var/lib/myapp/initialized"
    depends_on: Install myapp
```

### Background execution

Long-running commands like security scans can run in the background so they don't block convergence. The command is spawned and the resource returns immediately. The agent tracks running background processes by resource name — if a previous instance is still running when the next convergence cycle fires, the resource is skipped automatically. This prevents accumulation of duplicate long-running processes. Pair with `onlyif` or `unless` to prevent re-spawning across days or reboots.

```yaml
resources:
  - name: Run trivy scan
    type: exec
    command: "trivy rootfs --format json -o /var/lib/vigo/security/trivy.json / && touch /tmp/.vigo-trivy-$(date +%Y%m%d)"
    onlyif: "! test -f /tmp/.vigo-trivy-$(date +%Y%m%d)"
    background: "true"
```

Background execs auto-nice to CPU priority 19 and I/O best-effort lowest. To use a different priority, set `nice:` explicitly:


```yaml
resources:
  - name: Heavy data export
    type: exec
    command: /opt/myapp/bin/export-all --output /var/lib/exports/
    nice: "10"
```

### With timeout

Override the default 300-second timeout for long-running operations:

```yaml
resources:
  - name: Full database backup
    type: exec
    command: pg_dump -Fc mydb > /var/backups/mydb.dump
    timeout: "900"
```

### With retries

Retry flaky commands (e.g., network-dependent operations). Only the command is retried — guards are not re-evaluated:

```yaml
resources:
  - name: Pull container image
    type: exec
    command: docker pull registry.example.com/myapp:latest
    retries: "3"
    retry_delay: "10"
    unless: "docker image inspect registry.example.com/myapp:latest >/dev/null 2>&1"
```

### With sensitive

Suppress the command from result names and error messages. Use for commands that contain interpolated secrets or sensitive paths:

```yaml
resources:
  - name: Rotate database credentials
    type: exec
    command: /opt/myapp/bin/rotate-creds --token={{api_token}}
    sensitive: "true"
```

When `sensitive: true` and the command fails, the error message shows the exit code but not the command or its output: `exec command failed (exit 1) — command suppressed (sensitive: true)`.

### With shell override

Use a specific shell instead of the default `sh`:

```yaml
resources:
  - name: Run bash-specific script
    type: exec
    command: "shopt -s globstar; for f in /etc/myapp/**/*.conf; do validate-config $f; done"
    shell: bash
```

The shell override also applies to guard commands (`onlyif`/`unless`).

## Platform

Cross-platform. Commands are executed via `sh -c` on Unix and `cmd /C` on Windows. Use the `shell` parameter to override (e.g., `bash`, `zsh`, `fish`).

### Trigger-only execution

Use `when: "changed"` to only run the command when triggered by `notify`, `subscribes`, or `watch_secret`:

```yaml
resources:
  - name: Set Grafana admin password
    type: exec
    command: grafana-cli admin reset-admin-password --password-from-stdin
    stdin: "secret:grafana/admin-password"
    watch_secret: ["grafana/admin-password"]
    when: "changed"
```

The command is skipped on normal check-ins and only runs when the watched secret is rotated or when another resource triggers it via notify. `when: "changed"` can be combined with other expressions: `when: "changed && os_family('debian')"`.

## When NOT to Use exec

Use a specialized executor instead of `exec` whenever one exists. Specialized executors provide idempotency, cross-platform support, and error handling that raw shell commands don't.

| Instead of... | Use... |
|---------------|--------|
| `exec: "apt-get install nginx"` | `type: package` — handles idempotency, version checking, and works on apt/dnf/yum/brew/pkg |
| `exec: "systemctl restart nginx"` | `type: service` — manages state (running/stopped), enable/disable, works across systemd/launchd/rcctl/SMF |
| `exec: "useradd -m deploy"` | `type: user` — manages UID, shell, groups, authorized_keys, detects drift |
| `exec: "git clone ..."` | `type: git` — tracks branch HEAD, updates on drift |
| `exec: "echo '0 * * * * root /opt/task.sh' > /etc/cron.d/task"` | `type: cron` — manages schedule fields, validates syntax |
| `exec: "docker compose up -d"` | `type: docker_compose` — handles pull, build, orphan removal |

`exec` is appropriate for operations with no dedicated executor — database migrations, one-time initialization scripts, custom health checks, or calling vendor-specific CLI tools.

## Notes

- The `stdin` and `secret_file` parameters are mutually exclusive. Using both is an error.
- `secret_file` creates a temporary file with mode 0600, writes the secret, replaces `{{secret_file}}` in the command, runs the command, then deletes the file. The secret never appears in command arguments or logs.
- If no guard is provided and no `when: "changed"` is set, the command always runs (always returns `changed: true`).
- `sensitive` and `logoutput` are complementary: `logoutput: false` suppresses stdout/stderr from errors while still showing the command string; `sensitive: true` suppresses the command string itself (and all output on failure). Both can be used together.
- `retries` wraps only the command execution. Guards (`onlyif`/`unless`) run once before any attempts. Background execs (`background: true`) are never retried.
- The `shell` parameter sets the shell for both the main command and guard commands. When `user` is set, commands run via `su -c` regardless of shell override.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
