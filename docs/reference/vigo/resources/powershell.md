
# powershell

Runs PowerShell scripts with proper error handling and idempotency guards. Unlike the generic [`exec`](exec.md) executor, this executor always uses `powershell.exe` with `-NoProfile -NonInteractive -ExecutionPolicy Bypass` and automatically sets `$ErrorActionPreference = 'Stop'` so that PowerShell exceptions are mapped to resource failures.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `command` | Yes | -- | PowerShell script or command to execute. |
| `unless` | No | -- | PowerShell command. If it succeeds (exit 0), the resource is skipped. |
| `onlyif` | No | -- | PowerShell command. If it fails (non-zero exit), the resource is skipped. |
| `cwd` | No | -- | Working directory for the command. |
| `environment` | No | -- | Comma-separated `KEY=VALUE` pairs to set as environment variables. |
| `revert` | No | `false` | When `true`, run `on_revert` to undo this resource instead of applying. See [Reversal](#reversal). |
| `on_revert` | No | -- | PowerShell command run (via `powershell.exe`, same flags as `command`) to reverse this resource. Required when `revert: true`. |

## Idempotency

The executor supports three guard mechanisms, evaluated in order:

1. **`unless`** -- Skip if the guard command succeeds (exit code 0). Use this to check whether the desired state already exists (e.g., `unless: "Test-Path 'C:\\path\\to\\marker'"`).
2. **`onlyif`** -- Skip if the guard command fails (non-zero exit). Use this to check whether a prerequisite is met before running.

If none of the guards are specified, the command runs on every convergence. Always use at least one guard to maintain idempotency.

## Reversal

`powershell` runs an arbitrary script with no inferable inverse, so reversal is **operator-declared**: pair the resource with an `on_revert:` command. The inverse runs via `powershell.exe` (same flags as `command`, with the idempotency guards stripped so it runs unconditionally), not via `sh -c`.

- `on_revert:` — a PowerShell command that undoes the resource. Inert until triggered.
- `revert: true` runs `on_revert:` once, then reports settled on subsequent runs (idempotent — it never re-runs). `revert: true` with no `on_revert:` is rejected at `config publish`.
- A normal (non-revert) apply clears a spent revert, re-arming it.

Removing the resource from config does **not** undo it — that only stops enforcement. Use `revert: true` with a declared `on_revert:` to actively undo.

## Examples

### Initialize a database

```yaml
resources:
  - name: init-database
    type: powershell
    command: "& C:\\Scripts\\init-db.ps1 -Server localhost -Database MyApp"
    unless: "Test-Path 'C:\\ProgramData\\MyApp\\db-initialized.flag'"
```

### Install a certificate

```yaml
resources:
  - name: import-cert
    type: powershell
    command: |
      Import-PfxCertificate -FilePath C:\certs\app.pfx `
        -CertStoreLocation Cert:\LocalMachine\My `
        -Password (ConvertTo-SecureString 'changeit' -AsPlainText -Force)
    unless: "Get-ChildItem Cert:\\LocalMachine\\My | Where-Object { $_.Subject -match 'CN=myapp' }"
```

### Run with environment variables

```yaml
resources:
  - name: deploy-app
    type: powershell
    command: "& C:\\Scripts\\deploy.ps1"
    cwd: "C:\\Deployments\\myapp"
    environment: "APP_ENV=production,DB_HOST=sql01.internal"
    unless: "Test-Path 'C:\\Deployments\\myapp\\deployed.flag'"
```

### Conditional execution with onlyif

```yaml
resources:
  - name: join-domain
    type: powershell
    command: |
      Add-Computer -DomainName corp.example.com `
        -Credential (New-Object PSCredential('admin', (ConvertTo-SecureString 'secret:corp/join-pass' -AsPlainText -Force)))
    onlyif: "if ((Get-WmiObject Win32_ComputerSystem).PartOfDomain) { exit 1 } else { exit 0 }"
```

### Configure IIS application pool

```yaml
resources:
  - name: configure-app-pool
    type: powershell
    command: |
      Import-Module WebAdministration
      Set-ItemProperty IIS:\AppPools\MyApp -Name processModel.identityType -Value 3
      Set-ItemProperty IIS:\AppPools\MyApp -Name processModel.userName -Value 'svc_myapp'
    unless: |
      Import-Module WebAdministration
      $pool = Get-ItemProperty IIS:\AppPools\MyApp -Name processModel
      if ($pool.identityType -eq 3 -and $pool.userName -eq 'svc_myapp') { exit 0 } else { exit 1 }
```

## Platform

Windows only.

## Notes

- `$ErrorActionPreference = 'Stop'` is prepended automatically. Any unhandled PowerShell exception causes the resource to fail. Do not override this unless you understand the implications.
- The `-ExecutionPolicy Bypass` flag allows scripts to run regardless of the system's execution policy.
- Long command strings (over 60 characters) are truncated in resource result names for readability.
- The `environment` parameter sets variables only for the duration of the command. For persistent environment variables, use the [`windows_env`](windows_env.md) executor.
- Guard commands (`unless`, `onlyif`) run under the same PowerShell flags and environment as the main command.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
