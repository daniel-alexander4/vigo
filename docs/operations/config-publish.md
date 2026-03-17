# Config Publish Pipeline

Operators edit `.hnb` files in a proposed directory. The `config publish` command validates them using the same logic as the server's config loader, detects changes, then atomically syncs to the hidden `.live` directory the server reads from.

## Directory Layout

```
/srv/hobnob/hnb/
├── proposed/                 ← operators edit here
│   ├── modules/
│   ├── roles/
│   ├── thralls/
│   ├── templates/            (optional)
│   └── vars/                 (optional)
└── .live/                    ← server reads here (hidden, do not edit)
    ├── modules/
    ├── roles/
    ├── thralls/
    ├── templates/
    └── vars/
```

## Usage

```bash
# Validate without publishing
hobnobcli config publish --dry-run

# Validate and publish
hobnobcli config publish

# Skip URL checks (air-gapped environments)
hobnobcli config publish --skip-url-check
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Validate only, do not sync |
| `--skip-url-check` | `false` | Skip HTTP aliveness checks for URLs |
| `--url-timeout` | `10s` | Timeout per URL aliveness check |
| `--ask-ai` | `false` | Ask AI to suggest fixes for validation errors |

## How It Works

1. Diffs `proposed/` against `.live/` using SHA-256 hashes to find new, changed, and removed files
2. Loads all `.hnb` files from `proposed/` using `config.NewConfigStore()` — the same code path the server uses. Full tree loading is required because modules, roles, and thrall entries cross-reference each other.
3. HTTP/HTTPS URLs in **changed modules only** are probed for aliveness (HEAD with GET fallback). Unchanged modules are skipped.
4. If validation passes and `--dry-run` is not set, atomically syncs `modules/`, `roles/`, `thralls/`, `templates/`, and `vars/` to `.live/`
5. The CLI calls `POST /api/v1/config/reload` to trigger a server-side config reload
6. If nothing changed, the command reports "Nothing to publish" and does not touch `.live/`

## Atomic Sync

Each subdirectory is synced independently using a rename strategy:

1. Copy proposed subdir to a temp directory inside `.live/` (`.publish-tmp-*`)
2. Rename existing `.live/` subdir to a backup (`.publish-backup-*`)
3. Rename temp to live
4. Remove backup on success; rollback on failure

Subdirectories not present in proposed are removed from `.live/` (sync is replacement, not additive).

## What Gets Validated

### Structural (via `NewConfigStore`)

- YAML syntax
- Module and role references (no dangling refs)
- Circular dependency detection
- Resource idempotency (exec resources need `creates`, `onlyif`, or `unless`)
- Cross-resource references (`depends_on`, `notify`, `subscribes`, `before` must reference existing resource names)
- Redundant triggers (resource A `notify: [B]` and B `subscribes: [A]` — double trigger)
- Unknown resource types (blocked unless `custom: true` is set on the resource)
- `file` with `state: directory` (must use `type: directory` instead)
- `service` with `state: restarted/reloaded` without `subscribes` (restarts every convergence)
- `reboot` without `subscribes` or `reboot_required_file` (reboots every convergence)
- Secret reference format (not secret availability)
- Template syntax in `content:` attributes
- `when:` expression parsing
- Absolute path validation for `targetpath` and `path` fields
- Octal mode format validation
- Service state enum (`running`, `stopped`, `restarted`, `reloaded`)
- Required fields: archive (`url`), git (`repo`, `path`), mount (`path`)

### URL Aliveness (changed modules only)

- HTTP HEAD probe for all http/https URLs in: `archive.url`, `repository.key_url`, `git.repo`
- Falls back to GET with Range header if HEAD returns 405
- 4xx/5xx or connection failure = validation error
- Non-HTTP URLs (ssh://, git://) are skipped
- Disabled with `--skip-url-check`

## AI-Assisted Fix Suggestions

When validation fails, `config publish` calls the AI assistant (if the server is running with AI enabled) to explain each error and suggest corrected YAML. Suggestions stream to stderr below the error output.

Add `--ask-ai` to ask the AI assistant to suggest fixes for each error. Requires the server to be running with AI enabled.

## File Permissions

The `.live/` directory is protected so operators cannot bypass validation by writing directly:

- **`.live/`** — `0750` (root can read/write, group can read)
- **Files in `.live/`** — `0640` (root can read/write, group can read)
- **`proposed/`** — `0770` (operators can read/write)

Permissions are enforced automatically after each publish. The server logs a warning at startup if `.live/` is world-writable.

To set up the recommended permission model:

```bash
# Create hobnob group for server process
groupadd hobnob

# Lock down .live (only root/publish can write, hobd can read via group)
chown -R root:hobnob /srv/hobnob/hnb/.live
chmod 750 /srv/hobnob/hnb/.live

# Operators can write to proposed
chmod 770 /srv/hobnob/hnb/proposed
```

## Custom Executor Resources

Unknown resource types are blocked by default. If a resource uses a custom executor, add `skip_validation: true` to bypass type validation:

```yaml
- name: deploy-app
  type: capistrano
  skip_validation: true
  command: "cap production deploy"
  creates: "/var/www/app/current"
```

## First-Time Setup

On a fresh install, `.live/` is empty. The server starts gracefully with zero modules and zero thrall entries. Place your configs in `proposed/` and run `hobnobcli config publish` to populate `.live/` for the first time.

## Output

```bash
$ hobnobcli config publish --dry-run
Validation passed: 12 modules, 3 roles, 8 thrall entries
  + modules/nginx.hnb
  + modules/sshd.hnb
  + thralls/web.hnb

$ hobnobcli config publish
Published: 12 modules, 3 roles, 8 thrall entries (15 files synced)
  + modules/nginx.hnb
  + modules/sshd.hnb
  + thralls/web.hnb
```

## Integration with Change Approval

When `publish.approval_required: true` is set in `server.yaml`, the publish flow adds an approval gate before configs go live. Instead of immediately syncing to `.live/` and triggering a reload, `hobnobcli config publish` creates a pending change request. The validated configs are staged but not activated.

A second admin must then run `hobnobcli config approve <id>` to activate the change. This enforces two-person review for all config changes reaching production.

If `approval_required` is `false` (the default), the publish flow works exactly as described above with no approval gate.

See [Change Approval](change-approval.md) for full details on the approval workflow, expiration, and rejection.
