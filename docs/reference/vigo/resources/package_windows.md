
# package_windows

Manages Windows packages using the first available package manager: Chocolatey, winget, or Scoop.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `package` | Yes | -- | Package name/ID. |
| `state` | Yes | -- | Desired state: `present` or `absent`. |
| `version` | No | -- | `latest` keeps the package at the newest version the backend offers (install if missing, upgrade otherwise). Specific-version pinning is not supported by this executor. |

## States

- **`present`** -- Ensure the package is installed. With `version: latest`, install if not present, or upgrade if already installed.
- **`absent`** -- Uninstall the package.

`state: latest` was retired — declare `version: latest` instead.

## Package Manager Detection

The executor auto-detects the available package manager in priority order:

1. **Chocolatey** (`choco.exe`) -- `choco install -y --no-progress`
2. **winget** (`winget.exe`) -- `winget install --exact --id <name> --silent`
3. **Scoop** (`scoop.cmd`) -- `scoop install <name>`

If none are found, the executor returns an error.

## Idempotency

Queries the detected package manager to check if the package is installed and its current version before acting.

## Examples

### Basic

```yaml
resources:
  - name: Install Git
    type: package
    package: Git.Git
```

### Ensure latest

```yaml
resources:
  - name: Keep 7-Zip updated
    type: package
    package: 7zip
    version: latest
    state: present
```

### Uninstall

```yaml
resources:
  - name: Remove old package
    type: package
    package: legacy-tool
    state: absent
```

## Platform

Windows only. On Linux, `type: package` maps to the [package](package.md) executor (APT/DNF/etc.).

## Notes

- Package names depend on the backend. Use Chocolatey package names with `choco`, winget IDs with `winget`, and Scoop bucket names with `scoop`.
- The executor uses `where.exe` to probe for available package managers.
- Chocolatey operations include `--no-progress` to suppress progress bars. winget operations include `--accept-source-agreements --accept-package-agreements`.
