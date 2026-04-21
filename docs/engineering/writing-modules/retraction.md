---
title: Module Retraction
weight: 50
---

# Module Retraction

Module retraction automatically generates `.retract` files --- the reverse of each module --- so you always have a clean undo ready. When enabled, `vigocli config publish` generates a retract module for every new or changed module, and shows affected envoys when a module is removed.

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

### On new or changed modules

1. Run `vigocli config publish` after adding or modifying a module in `stockpile/modules/`
2. The publish pipeline detects new/changed modules and reads their content
3. The server generates a deterministic retract module (flipping each resource to its absent state)
4. The AI reviews and improves the draft, especially for `exec` resources where reversal requires reasoning
5. A `.retract` file is written alongside the module in `stockpile/modules/`

### On removed modules

1. Run `vigocli config publish` after removing a module from `stockpile/modules/`
2. The publish pipeline detects the removal and reads the module content from the previous config
3. The server generates the retract module and looks up which envoys previously ran it
4. A `.retract` file is written to `stockpile/modules/` with a detailed summary of affected envoys and per-resource confidence levels

## Generated Output

The generated `.retract` file is inert --- it won't be loaded by the config system until renamed to `.vgo`. For removed modules, the CLI also prints:

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
| `source_package` | Delete target file | Medium (extracted contents not removed) |
| `nonrepo_package` | `state: absent` (remove via dpkg/rpm) | High |
| `exec` | AI-generated reversal with `onlyif` guard | Low (always review) |

## Applying a Retraction

1. Review the generated file: `stockpile/modules/<name>.retract`
2. Rename to `<name>-retract.vgo`
3. Add module `<name>-retract` to the relevant match blocks or roles
4. Run `vigocli config publish`
5. After all affected envoys converge, remove the retract module and its match block

The retract module is idempotent --- it's safe to leave in the config for multiple convergence cycles while all envoys catch up. Remove it once cleanup is confirmed.

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

Returns generated retract YAML, warnings, and affected envoy list for each module.

## Example Retractions

Every example module in `example-configs/stockpile/modules/` has a pre-built retraction counterpart in `example-configs/retractions/modules/`. These follow the same reversal rules documented above and serve as reference implementations.

To use an example retraction:

1. Copy the retraction to your stockpile:
   ```bash
   cp example-configs/retractions/modules/web/nginx-retract.vgo.example \
      /srv/vigo/stockpile/modules/nginx-retract.vgo
   ```
2. Review the file, especially any `# REVIEW:` or `# WARNING:` comments
3. Add `nginx-retract` to the relevant match blocks or roles
4. Publish: `vigocli config publish`
5. After all affected envoys converge, remove the retract module and its match block
