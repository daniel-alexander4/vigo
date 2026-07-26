
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
| `nice` | No | `0` | CPU/IO priority level 0–19 (0 = normal, 19 = lowest). Background execs default to 19. On Linux, also sets I/O priority: `nice >= 15` drops into the ionice `idle` class (only runs I/O when nothing else wants the disk); `nice < 15` uses ionice best-effort with a proportional level. |
| `timeout` | No | -- | Per-process execution ceiling. No operator-facing default — but when unset, foreground execs still inherit the runner's per-resource default ceiling (300s for `exec`) so a wedged child can't freeze the convergence cycle; set `timeout:` explicitly to raise or lower it. Accepts a bare integer (seconds) or a duration suffix: `s`, `m`, `h` (e.g., `"30s"`, `"45m"`, `"4h"`). On expiry the agent SIGCONTs the child's process group (so a stopped child can act on what follows), SIGTERMs it, waits 5s, then SIGKILLs. Background execs are exempt from the default and run unbounded unless `timeout:` is set; they release their PID tracking on timeout so the next convergence cycle is free to respawn. |
| `retries` | No | `0` | Number of retry attempts on failure. Only the command is retried — guards are not re-evaluated. Background execs are not retried. |
| `retry_delay` | No | `5` | Seconds to wait between retry attempts. |
| `sensitive` | No | `false` | When `true`, suppress the command string from result names, logs, and error messages. Distinct from `logoutput: false` which only suppresses stdout/stderr. |
| `shell` | No | `sh` | Shell to use for command execution (e.g., `bash`, `zsh`, `fish`). Default is `sh` on Unix, `cmd` on Windows. Guards also use the specified shell. |
| `report_settled` | No | `false` | When `true` and the command exits with an acceptable code, the agent reports `Action::None` (Settled) and `changed=false` instead of `Action::Created`. Operator's escape hatch for exec resources whose successful execution is maintenance / cache-warming rather than a real state change — keeps daily refreshers (e.g., `apt-get update`) from inflating `ChangedCount` and tripping drift detection. Failures are unaffected: a non-zero exit still surfaces normally with the error populated. |
| `revert` | No | `false` | When `true`, run `on_revert` to undo this resource instead of applying. See [Reversal](#reversal). |
| `on_revert` | No | -- | Shell command run locally to reverse this resource. Required when `revert: true`. |

## States

The exec executor does not use a `state` parameter. It either runs or is skipped based on guards.

## Reversal

`exec` performs an action with no inferable inverse, so reversal is **operator-declared**: pair the resource with an `on_revert:` command.

- `on_revert:` — a shell command, run locally on the agent, that undoes the resource. Inert until triggered.
- `revert: true` runs `on_revert:` once, then reports settled on subsequent runs (idempotent — it never re-runs). `revert: true` with no `on_revert:` is rejected at `config publish`. Set `on_revert: none` to declare the action has **no inverse** — it is then omitted from configcrate retraction (`state: absent`) rather than reverted, and `revert: true` + `on_revert: none` is rejected as a contradiction.
- A normal (non-revert) apply clears a spent revert, re-arming it.

Removing the resource from config does **not** undo it — that only stops enforcement. Use `revert: true` with a declared `on_revert:` to actively undo.

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

### Cache refresh / maintenance steps

`report_settled` exists for exec resources whose *successful execution
is the work* but doesn't change persistent state in a way the dashboard
should care about — package-cache refreshes, `daemon-reload` after a
config write, the second half of a notify chain. The command runs as
normal and its `onlyif` / `unless` gates still apply; only the per-run
result label changes from `Created` to `Settled` on success. A failed
exec still surfaces as a real failure regardless of the flag.

```yaml
resources:
  - name: apt-update
    type: exec
    command: apt-get update -qq
    onlyif: find /var/lib/apt/lists -maxdepth 0 -mmin +1440 2>/dev/null | grep -q . || ! test -d /var/lib/apt/lists
    report_settled: true   # daily cache warmer; not a state change
```

Without `report_settled` the resource above would emit a `Created`
result every day the gate fired, contributing to the envoy's
`ChangedCount` and eventually classifying it as `Diverged` on the drift
axis after `convergence.DivergedThreshold` consecutive runs. With the
flag set, the run is reported as Settled (grey muted badge), drift
detection sees no change, the dashboard stays clean.

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

Kill the command (and its whole process group) if it runs longer than the
given duration. When unset, a foreground exec falls back to the runner's
per-resource default ceiling (300s for `exec`); set `timeout:` to raise it for
long-running work like a full backup, or lower it for a quick command. Background
execs are exempt from the default — set `timeout:` to keep a wedged scan from
lingering across cycles.

```yaml
resources:
  - name: Full database backup
    type: exec
    command: pg_dump -Fc mydb > /var/backups/mydb.dump
    timeout: "15m"
```

Bare integers are treated as seconds (`timeout: "900"` ≡ `timeout: "15m"`);
suffixes `s`, `m`, `h` are accepted for readability.

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

## Guardrails

The agent refuses to run an `exec` whose command would damage Vigo itself. A refusal is logged as `GUARDRAIL BLOCKED` with a reason and the resource is reported failed; the guardrails are hardcoded in the agent and cannot be relaxed from the server.

Two checks apply to `command`:

- **Dangerous patterns** — literal strings that stop, delete, or kill the agent, server, or CLI (`systemctl stop vigo-envoy`, `pkill vigo`, `rm -rf /etc/vigo`, …).
- **Deletion near a protected path** — if the command contains `rm ` or `unlink ` *anywhere*, the **entire command string** is scanned for Vigo's protected paths (`/var/lib/vigo/`, `/etc/vigo/`, `/usr/local/sbin/vigo`, …) and blocked on any match.

The second check is deliberately fail-closed and does **not** honor the exempt-path carve-out that structural `file`/`directory` resources get for `/var/lib/vigo/security/`. In a free-form command the real target of the `rm` can't be isolated from the rest of the string, so an exempt-substring escape would itself be a bypass (`rm -rf /var/lib/vigo && ls /var/lib/vigo/security`).

The practical consequence: a command that writes output under `/var/lib/vigo/security/` **and** cleans up a temp directory in the same shell line is blocked, even though both halves are individually legal.

```yaml
# Blocked — the `rm` makes the whole string subject to the path scan, and
# --output matches /var/lib/vigo/.
- name: run-scanner
  type: exec
  command: sh -c 'scanner --output /var/lib/vigo/security/out.json / && rm -rf /tmp/scanner-*'

# Allowed — split the cleanup into its own resource that names no protected path.
- name: run-scanner
  type: exec
  command: sh -c 'scanner --output /var/lib/vigo/security/out.json /'

- name: run-scanner-cleanup
  type: exec
  command: sh -c 'rm -rf /tmp/scanner-*'
  depends_on: [run-scanner]
```

Prefer a structural `file` or `directory` resource over a shell `rm` wherever one fits — those are checked against the resolved target and do honor the exempt path.

## When NOT to Use exec

Use a specialized executor instead of `exec` whenever one exists. Specialized executors provide idempotency, cross-platform support, and error handling that raw shell commands don't.

| Instead of... | Use... |
|---------------|--------|
| `exec: "apt-get install nginx"` | `type: package` — handles idempotency, version checking, and works on apt/dnf/yum/brew/pkg |
| `exec: "systemctl restart nginx"` | `type: service` — manages state (running/stopped), enable/disable, works across systemd/launchd/rcctl/SMF |
| `exec: "useradd -m deploy"` | `type: user` — manages UID, shell, groups, authorized_keys, detects drift |
| `exec: "git clone ..."` | `type: git` — tracks branch HEAD, updates on drift |
| `exec: "longdrawer '0 * * * * root /opt/task.sh' > /etc/cron.d/task"` | `type: cron` — manages schedule fields, validates syntax |
| `exec: "docker compose up -d"` | `type: container` per service — Vigo-native multi-container declaration with `depends_on` ordering, or keep the `exec:` wrapper with an `unless:` guard if the operator already has a maintained `docker-compose.yml` |

`exec` is appropriate for operations with no dedicated executor — database migrations, one-time initialization scripts, custom health checks, or calling vendor-specific CLI tools.

## Notes

- The `stdin` and `secret_file` parameters are mutually exclusive. Using both is an error.
- `secret_file` creates a temporary file with mode 0600, writes the secret, replaces `{{secret_file}}` in the command, runs the command, then deletes the file. The secret never appears in command arguments or logs.
- If no guard is provided and no `when: "changed"` is set, the command always runs (always returns `changed: true`).
- `sensitive` and `logoutput` are complementary: `logoutput: false` suppresses stdout/stderr from errors while still showing the command string; `sensitive: true` suppresses the command string itself (and all output on failure). Both can be used together.
- `retries` wraps only the command execution. Guards (`onlyif`/`unless`) run once before any attempts. Background execs (`background: true`) are never retried.
- The `shell` parameter sets the shell for both the main command and guard commands. When `user` is set, commands run via `su -c` regardless of shell override.
- **Commands never inherit a terminal.** Unless you set `stdin:`, the command's stdin is `/dev/null` and reads return EOF immediately. Commands must be non-interactive: a command that waits for input gets an empty read, not a prompt. This is also why an interactive command cannot hang a run — see [Progress reporting](#progress-reporting).

## Progress reporting

A command's output is captured, not streamed, so it appears only when the command finishes. For a command that runs for minutes — a large package upgrade, an image build — that would otherwise mean no feedback at all, so the agent samples the running process every 15 seconds and reports what it is doing:

```
still running  pid=6801 phase=dpkg state=D procs=3 elapsed_secs=134 io_delta_bytes=48291840
```

`phase` is the deepest running child process, which names the stage for free (`http` while a package manager downloads, `dpkg` while it unpacks). `state` is the process state, and it is the field worth reading:

| State | Meaning |
|---|---|
| `R` | Running on CPU. |
| `D` | Uninterruptible disk wait — **normal** for a package unpack on slow storage. Working, not hung. |
| `S` | Sleeping, typically waiting on the network. |
| `T` | **Stopped.** Never healthy; see below. |

These lines are logged at `info` when `vigo run` is attached to a terminal and at `debug` in the daemon, so the journal stays quiet. The command string itself is never logged here, so `sensitive: true` commands are unaffected. Nothing is emitted before the first 15-second tick, so ordinary short commands stay silent.

Two conditions are called out regardless of log level:

- **Stopped** — a process tree in state `T` with nothing runnable cannot resume on its own. The agent logs a warning and kills it immediately rather than waiting out `timeout:`. The usual cause is a child that tried to read the terminal; the second is an external `SIGSTOP`.
- **Stalled** — no CPU consumed and no bytes moved for 60 seconds. The agent warns but does **not** kill, because a command legitimately blocked on a slow network read looks identical. `timeout:` remains the backstop.

Progress detail comes from procfs and is Linux-only. On other platforms the heartbeat still reports elapsed time, without `phase`, `state`, or byte counts.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
