
# gem

Manages Ruby gems via the gem command. Supports version pinning and custom gem commands.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `package` | Yes | -- | Gem name. |
| `state` | No | `present` | Desired state: `present`, `absent`, or `latest`. |
| `version` | No | -- | Specific version to install. |
| `gem_cmd` | No | `gem` | The gem command to use. |
| `no_document` | No | `true` | If `true`, install with `--no-document` to skip documentation generation. Set to `false` to include docs. |

## States

- **`present`** -- Ensure the gem is installed. If `version` is specified and the installed version differs, install the specified version.
- **`absent`** -- Uninstall the gem (with `--executables --all`).
- **`latest`** -- Update the gem to the latest version (`gem update`).

## Idempotency

Runs `gem list --exact <name>` to check if the gem is installed and its current version. Compares against the desired state before acting.

## Examples

### Basic

```yaml
resources:
  - name: Install bundler
    type: gem
    package: bundler
```

### Specific version

```yaml
resources:
  - name: Install rails 7.0
    type: gem
    package: rails
    version: "7.0.0"
```

### Latest

```yaml
resources:
  - name: Keep rubocop up to date
    type: gem
    package: rubocop
    state: latest
```

### Uninstall

```yaml
resources:
  - name: Remove old gem
    type: gem
    package: old-gem
    state: absent
```

## Platform

Cross-platform. Requires Ruby and gem to be installed.

## Notes

- By default, `--no-document` is passed to skip RDoc/ri generation. Set `no_document: "false"` to include documentation.
- Uninstall removes all versions of the gem (`--all`) and associated executables (`--executables`).
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
