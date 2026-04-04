
# Config Publish Pipeline

Operators edit `.vgo` files in a stage directory. The `config publish` command validates them using the same logic as the server's config loader, detects changes, then atomically syncs to the hidden `.live` directory the server reads from.

## Directory Layout

```
/srv/vigo/
├── stage/                        ← operators edit here
│   ├── modules/                  # Module definitions
│   ├── templates/                # Template files for source: references
│   ├── roles.vgo                 # All role definitions
│   ├── common.vgo                # Inherited by all subdirs
│   ├── production/
│   │   ├── common.vgo            # Inherited by production subdirs
│   │   └── web/web.vgo           # Leaf entries with match:
│   └── staging/staging.vgo
└── .live/                        ← server reads here (hidden, do not edit)
```

The directory structure is both file organization and an inheritance hierarchy. See [Config Format: Directory Inheritance](../concepts/config-format.md#directory-inheritance) for details.

The legacy layout with `envoys/`, `roles/`, and `vars/` directories is still supported.

## Usage

```bash
# Validate without publishing
vigocli config publish --dry-run

# Validate and publish
vigocli config publish

# Skip URL checks (air-gapped environments)
vigocli config publish --skip-url-check
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Validate only, do not sync |
| `--skip-url-check` | `false` | Skip HTTP aliveness checks for URLs |
| `--url-timeout` | `10s` | Timeout per URL aliveness check |
| `--ask-ai` | `false` | Ask AI to suggest fixes for validation errors |

## How It Works

1. Normalizes whitespace in all `.vgo`/`.yaml` files (strip trailing spaces, fix line endings)
2. Diffs `stage/` against `.live/` using SHA-256 hashes to find new, changed, and removed files
3. Runs the **module linter** on changed/new module files (see below)
4. Loads all `.vgo` files from `stage/` using `config.NewConfigStore()` — the same code path the server uses. Full tree loading is required because modules, roles, and envoy entries cross-reference each other.
5. HTTP/HTTPS URLs in **changed modules only** are probed for aliveness (HEAD with GET fallback). Unchanged modules are skipped.
6. If validation passes and `--dry-run` is not set, atomically syncs `modules/`, `roles/`, `envoys/`, `templates/`, and `vars/` to `.live/`
7. The CLI calls `POST /api/v1/config/reload` to trigger a server-side config reload
8. If nothing changed, the command reports "Nothing to publish" and does not touch `.live/`

## Module Linter


Every changed or new module file is run through the module linter (`server/modlint`) before validation. The linter is a four-stage pipeline:

### 1. YAML Repair

Fixes common syntax issues at the text level:
- Tabs → 2 spaces
- Trailing whitespace removal
- Normalize line endings (CRLF → LF)
- Ensure final newline
- Unquoted YAML special values (`yes`/`no`/`on`/`off`) → `true`/`false`

### 2. Structure Normalization

- Canonical key ordering per resource type (e.g., `target_path` before `content` before `owner` for file resources)
- Consistent 2-space indentation
- Duplicate key handling (last wins per YAML spec)

### 3. Schema Validation

- Required fields per resource type (e.g., `file` needs `target_path`, `owner`, `group`, `mode`)
- Unknown resource types rejected (unless `skip_validation: true` is set)
- Cross-reference validation (`depends_on`, `notify`, `subscribes`, `before` must reference existing resource names)
- DAG cycle detection
- Idempotency checks (exec needs `onlyif`/`unless`, service `restarted`/`reloaded` needs `subscribes` or `when: "changed"`)
- Redundant trigger detection (`notify` + `subscribes` on the same pair)

### 4. Auto-Commenting

The linter adds `#~` prefixed auto-generated comments:

- **Module-level**: A block comment at the top describing what the module manages
- **Resource-level**: A single-line comment above each resource describing its action

```yaml
#~ Manages package, file, service
name: nginx

resources:
  #~ Install nginx package
  - name: nginx-package
    type: package
    package: nginx

  #~ Deploy /etc/nginx/nginx.conf
  - name: nginx-config
    type: file
    target_path: /etc/nginx/nginx.conf
    content: "server {}"
    owner: root
    group: root
    mode: "0644"
    notify: [nginx-service]

  #~ Ensure nginx is running and enabled
  - name: nginx-service
    type: service
    service: nginx
    state: running
    enabled: true
    depends_on: [nginx-package, nginx-config]
```

**Comment convention:**
- `#~` comments are auto-generated and **regenerated on every publish**. Never edit them — they will be overwritten.
- `#` comments (without the tilde) are hand-written and **never touched** by the linter. Use these for your own notes.
- Comments are derived from resource name, type, and attributes — they never restate what the YAML already says.

## Atomic Sync

Each subdirectory is synced independently using a rename strategy:

1. Copy stage subdir to a temp directory inside `.live/` (`.publish-tmp-*`)
2. Rename existing `.live/` subdir to a backup (`.publish-backup-*`)
3. Rename temp to live
4. Remove backup on success; rollback on failure

Subdirectories not present in stage are removed from `.live/` (sync is replacement, not additive).

## What Gets Validated

### Structural (via `NewConfigStore`)

- YAML syntax
- Module and role references (no dangling refs)
- Circular dependency detection
- Resource idempotency (exec resources need `onlyif` or `unless`)
- Cross-resource references (`depends_on`, `notify`, `subscribes`, `before` must reference existing resource names)
- Redundant triggers (resource A `notify: [B]` and B `subscribes: [A]` — double trigger)
- Unknown resource types (blocked unless `custom: true` is set on the resource)
- `file` with `state: directory` (must use `type: directory` instead)
- `service` with `state: restarted/reloaded` without `subscribes` or `when: "changed"` (restarts every convergence). If `subscribes` is present but `when: "changed"` is missing, the linter auto-adds it.
- `reboot` without `subscribes` or `reboot_required_file` (reboots every convergence)
- Secret reference format (not secret availability)
- Template syntax in `content:` attributes
- `when:` expression parsing
- Absolute path validation for `targetpath` and `path` fields
- Octal mode format validation
- Service state enum (`running`, `stopped`, `restarted`, `reloaded`)
- Required fields: source_package (`url`), git (`repo`, `path`), mount (`path`)

### URL Aliveness (changed modules only)

- HTTP HEAD probe for all http/https URLs in: `source_package.url`, `repository.key_url`, `git.repo`
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

- **`stage/`** — `0770` (operators can read/write)

Permissions are enforced automatically after each publish. The server logs a warning at startup if `.live/` is world-writable.

To set up the recommended permission model:

```bash
# Create vigo group for server process
groupadd vigo

# Lock down .live (only root/publish can write, vigosrv can read via group)
chown -R root:vigo /srv/vigo/.live
chmod 750 /srv/vigo/.live

# Operators can write to stage
chmod 770 /srv/vigo/stockpile
```

## Custom Executor Resources

Unknown resource types are blocked by default. If a resource uses a custom executor, add `skip_validation: true` to bypass type validation:

```yaml
- name: deploy-app
  type: capistrano
  skip_validation: true
  command: "cap production deploy"
  unless: "test -f /var/www/app/current"
```

## Startup Behavior with Invalid Config

If the server starts with invalid config in `.live/`, the behavior depends on the error type:

- **Idempotency errors** (unguarded exec, reboot without subscribes, etc.) — **non-fatal**. The server starts with zero modules loaded. The error is logged at ERROR level and stored in `LastReloadError()`, surfaced via `vigocli doctor` and the web dashboard. The REST API, UI, and gRPC remain available so you can diagnose and fix the config. Envoys get empty policy (no changes applied) until the config is repaired and republished.
- **Structural errors** (invalid YAML, dangling role→module references, secret resolution failures) — **fatal**. The server refuses to start. These indicate a fundamentally broken config tree.

Since `config publish` runs the module linter before syncing, most idempotency errors are caught before they reach `.live/`. The non-fatal startup behavior is a safety net for configs that bypass publish (e.g., placed directly in `.live/`).

## First-Time Setup

On a fresh install, `.live/` is empty. The server starts gracefully with zero modules and zero envoy entries. Place your configs in `stage/` and run `vigocli config publish` to populate `.live/` for the first time.

## Output

```bash
$ vigocli config publish --dry-run
Validation passed: 12 modules, 3 roles, 8 envoy entries
  + modules/nginx.vgo
  + modules/sshd.vgo
  + envoys/web.vgo

$ vigocli config publish
Published: 12 modules, 3 roles, 8 envoy entries (15 files synced)
  + modules/nginx.vgo
  + modules/sshd.vgo
  + envoys/web.vgo
```

## Integration with Change Approval

When `publish.approval_required: true` is set in `server.yaml`, the publish flow adds an approval gate before configs go live. Instead of immediately syncing to `.live/` and triggering a reload, `vigocli config publish` creates a pending change request. The validated configs are staged but not activated.

A second admin must then run `vigocli config approve <id>` to activate the change. This enforces two-person review for all config changes reaching production.

If `approval_required` is `false` (the default), the publish flow works exactly as described above with no approval gate.

See [Change Approval](change-approval.md) for full details on the approval workflow, expiration, and rejection.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
