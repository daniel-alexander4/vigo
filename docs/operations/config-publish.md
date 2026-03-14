# Config Publish Pipeline

Operators edit `.hnb` files in a proposed directory. The `config publish` command validates them using the same logic as the server's config loader, detects changes, then atomically syncs to the hidden `.live` directory the server reads from.

## Directory Layout

```
/etc/hobnob-server/hnb/
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
| `--dangerous` | `false` | Allow non-idempotent resources (exec without guards) |
| `--skip-url-check` | `false` | Skip HTTP aliveness checks for URLs |
| `--url-timeout` | `10s` | Timeout per URL aliveness check |

## How It Works

1. Diffs `proposed/` against `.live/` using SHA-256 hashes to find new, changed, and removed files
2. Loads all `.hnb` files from `proposed/` using `config.NewConfigStore()` — the same code path the server uses. Full tree loading is required because modules, roles, and thrall entries cross-reference each other.
3. HTTP/HTTPS URLs in **changed modules only** are probed for aliveness (HEAD with GET fallback). Unchanged modules are skipped.
4. If validation passes and `--dry-run` is not set, atomically syncs `modules/`, `roles/`, `thralls/`, `templates/`, and `vars/` to `.live/`
5. The server's fsnotify watcher picks up the changes and reloads
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
- Resource idempotency (exec resources need `creates`, `onlyif`, or `unless` — unless `--dangerous`)
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
