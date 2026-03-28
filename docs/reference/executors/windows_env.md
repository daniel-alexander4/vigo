
# windows_env

Manages persistent Windows environment variables idempotently via the .NET `[Environment]::SetEnvironmentVariable()` API. Changes are written to the registry and survive reboots.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `variable` | Yes | -- | Environment variable name (e.g., `JAVA_HOME`, `PATH`). |
| `value` | Yes (for present) | -- | Desired value for the variable. |
| `target` | No | `Machine` | Scope: `Machine` (system-wide, HKLM) or `User` (current user, HKCU). |
| `state` | No | `present` | `present` to set the variable, `absent` to remove it. |

## States

- **`present`** -- Ensure the variable exists with the specified value. Creates or updates as needed.
- **`absent`** -- Remove the variable entirely from the specified target scope.

## Idempotency

The executor reads the current value via `[Environment]::GetEnvironmentVariable()` before acting. If the variable already has the desired value, no change is made. When removing, if the variable does not exist, no action is taken.

## Examples

### Set JAVA_HOME

```yaml
resources:
  - name: set-java-home
    type: windows_env
    variable: JAVA_HOME
    value: "C:\\Program Files\\Java\\jdk-17"
```

### Set a user-scoped variable

```yaml
resources:
  - name: set-user-editor
    type: windows_env
    variable: EDITOR
    value: "C:\\Program Files\\Notepad++\\notepad++.exe"
    target: User
```

### Remove an environment variable

```yaml
resources:
  - name: remove-legacy-var
    type: windows_env
    variable: OLD_APP_HOME
    state: absent
```

### Application environment setup

```yaml
resources:
  - name: set-go-root
    type: windows_env
    variable: GOROOT
    value: "C:\\Go"

  - name: set-go-path
    type: windows_env
    variable: GOPATH
    value: "C:\\Users\\deploy\\go"

  - name: set-node-env
    type: windows_env
    variable: NODE_ENV
    value: production
```

## Platform

Windows only.

## Notes

- Changes to `Machine`-scoped variables require administrator privileges.
- Environment variable changes take effect for new processes only. Running processes retain their original environment until restarted.
- To modify `PATH`, set the full desired value. The executor does not support appending to existing values -- use the [`powershell`](powershell.md) executor for PATH manipulation if needed.
- Value comparison is exact (case-sensitive). If the current value differs in any way, the variable is updated.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
