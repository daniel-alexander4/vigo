---
title: Module Retraction
weight: 50
---

# Module Retraction

When you remove a module from the config and publish, the resources it managed (packages, files, services, etc.) are left behind on every envoy that had the module. Module retraction detects this during `vigocli config publish` and generates an idempotent uninstall module for operator review.

## Requirements

Both settings must be enabled in `server.yaml`:

```yaml
ai:
  enabled: true
  provider: claude    # or openai, ollama

publish:
  retraction:
    enabled: true
```

## How It Works

1. Run `vigocli config publish` after removing a module from `stockpile/modules/`
2. The publish pipeline detects the removed module and reads its content from the previous config
3. The server generates a deterministic uninstall module (flipping each resource to its absent state)
4. The AI reviews and improves the draft, especially for `exec` resources where reversal requires reasoning
5. A `.uninstall` file is written to `stockpile/modules/` and a summary is printed

## Generated Output

The generated `.uninstall` file is inert --- it won't be loaded by the config system until renamed to `.vgo`. The CLI prints:

- Which envoys previously ran the module
- Per-resource confidence levels (high for package/file/service, low for exec)
- Step-by-step instructions for applying the retraction

## Resource Reversal Rules

| Resource Type | Reversal | Confidence |
|---|---|---|
| `file`, `directory`, `symlink` | `state: absent` | High |
| `package` | `state: absent` | High |
| `service` | `state: stopped`, `enabled: false` | High |
| `user` | `state: absent` | Medium (home dir may have data) |
| `cron` | `state: absent` | High |
| `repository` | `state: absent` | High |
| `sysctl` | `state: absent` (resets to system default) | High |
| `firewall` | `state: absent` | High |
| `archive` | Delete target file | Medium (extracted contents not removed) |
| `exec` | AI-generated reversal with `onlyif` guard | Low (always review) |

## Applying a Retraction

1. Review the generated file: `stockpile/modules/<name>.uninstall`
2. Rename to `<name>-uninstall.vgo`
3. Add module `<name>-uninstall` to the relevant node entries or roles
4. Run `vigocli config publish`
5. After all affected envoys converge, remove the uninstall module and its node entry

The uninstall module is idempotent --- it's safe to leave in the config for multiple convergence cycles while all envoys catch up. Remove it once cleanup is confirmed.

## API

**`POST /api/v1/config/retract`**

Called automatically by `vigocli config publish`. Can also be called directly:

```json
{
  "modules": [
    {"name": "nginx", "content": "name: nginx\nresources:\n  ..."}
  ]
}
```

Returns generated uninstall YAML, warnings, and affected envoy list for each module.
