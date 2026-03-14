# pip

Manages Python packages via pip. Supports version pinning, virtualenvs, and custom pip commands.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `package` | Yes | -- | Package name. Also accepts `name`. |
| `state` | No | `present` | Desired state: `present`, `absent`, or `latest`. |
| `version` | No | -- | Specific version to install (e.g., `2.28.0`). |
| `extra_args` | No | -- | Additional arguments passed to `pip install` (space-separated). |
| `pip_cmd` | No | `pip3` | The pip command to use. |
| `virtualenv` | No | -- | Path to a virtualenv. If set, uses `<virtualenv>/bin/<pip_cmd>`. |

## States

- **`present`** -- Ensure the package is installed. If `version` is specified and the installed version differs, upgrade to the specified version.
- **`absent`** -- Uninstall the package.
- **`latest`** -- Upgrade the package to the latest version (`pip install --upgrade`).

## Idempotency

Runs `pip show <package>` to check if the package is installed and its current version. Compares against the desired state and version before acting.

## Examples

### Basic

```yaml
resources:
  - name: Install requests
    type: pip
    package: requests
```

### Specific version

```yaml
resources:
  - name: Install requests 2.28.0
    type: pip
    package: requests
    version: "2.28.0"
```

### In a virtualenv

```yaml
resources:
  - name: Install Flask in app venv
    type: pip
    package: flask
    virtualenv: /opt/myapp/venv
```

### Latest version

```yaml
resources:
  - name: Keep boto3 up to date
    type: pip
    package: boto3
    state: latest
```

### Uninstall

```yaml
resources:
  - name: Remove deprecated package
    type: pip
    package: old-package
    state: absent
```

## Platform

Cross-platform. Requires pip to be installed.

## Notes

- The `extra_args` parameter is split on whitespace and appended to the `pip install` command.
- When using `virtualenv`, the pip binary path is constructed as `<virtualenv>/bin/<pip_cmd>`.
- The `latest` state always runs `pip install --upgrade`, so it always reports `changed: true`.
