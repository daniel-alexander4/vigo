# npm

Manages Node.js packages globally via npm.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `package` | Yes | -- | Package name. Also accepts `name`. |
| `state` | No | `present` | Desired state: `present`, `absent`, or `latest`. |
| `version` | No | -- | Specific version to install. |
| `npm_cmd` | No | `npm` | The npm command to use. |

## States

- **`present`** -- Ensure the package is installed globally. If `version` is specified and the installed version differs, install the specified version.
- **`absent`** -- Uninstall the package globally.
- **`latest`** -- Install or upgrade to the latest version (`npm install -g <package>@latest`).

## Idempotency

Runs `npm list -g <package> --depth=0 --json` to check if the package is installed and its current version. Parses the JSON output to compare versions before acting.

## Examples

### Basic

```yaml
resources:
  - name: Install yarn
    type: npm
    package: yarn
```

### Specific version

```yaml
resources:
  - name: Install typescript 5.0
    type: npm
    package: typescript
    version: "5.0.0"
```

### Latest

```yaml
resources:
  - name: Keep npm up to date
    type: npm
    package: npm
    state: latest
```

### Uninstall

```yaml
resources:
  - name: Remove create-react-app
    type: npm
    package: create-react-app
    state: absent
```

## Platform

Cross-platform. Requires Node.js and npm to be installed.

## Notes

- All operations use the `-g` (global) flag.
- Version comparison is exact string matching, not semver.
