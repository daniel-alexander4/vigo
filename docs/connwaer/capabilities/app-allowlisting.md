
# Application Allowlisting

| | |
|---|---|
| **Machine name** | `app_allowlisting` |
| **Controls** | CE-MW-4 |
| **Platforms** | Linux, Windows |
| **Timeout** | default (5m) |

## What it verifies

Confirms that an application allowlisting framework is installed, running, and actively enforcing policy -- meaning only approved binaries are permitted to execute. On Linux this checks fapolicyd; on Windows it checks AppLocker. The Linux path includes an active verification step that writes and attempts to execute an unauthorized binary to prove the framework actually blocks it.

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `framework` | string | `"auto"` | Linux only. Which allowlisting framework to check. `"auto"` detects fapolicyd automatically. Set to `"fapolicyd"` to force it. |

## Thresholds

This capability has no configurable thresholds.

## Verification logic

### Linux

1. **Detect framework** -- If `framework` is `"auto"`, check whether `fapolicyd` exists on `PATH`. If `framework` is `"fapolicyd"`, use it directly. If no framework is found, the check fails immediately.

2. **Service status** -- Run `systemctl is-active fapolicyd`. If the service is not `active`, record a failure and stop.

3. **Enforcement mode** -- Run `fapolicyctl status` and look for `"enforcing"` in the output. If `fapolicyctl` is not available, fall back to reading `/etc/fapolicyd/fapolicyd.conf` and checking for `permissive = 0`. If the framework is not enforcing, record a failure.

4. **Rule count** -- Run `fapolicyctl list` and count the loaded rules. Recorded as evidence but does not cause pass/fail on its own.

5. **Unauthorized execution test** -- Write a minimal executable script to a temporary directory, set it executable (mode 0755), and attempt to run it. If fapolicyd is working correctly, execution should be blocked (permission denied or non-zero exit). If the binary runs successfully, record a failure -- the framework is not blocking unknown binaries.

### Windows

1. **AppLocker service** -- Query `AppIDSvc` via `sc.exe query`. If the service is not `RUNNING`, record a failure.

2. **Policy rules** -- Run `Get-AppLockerPolicy -Effective -Xml` via PowerShell and check for `RuleCollection` entries. If no rules are found, record a failure.

3. **Enforcement mode** -- Query the first `RuleCollection` enforcement mode via `Get-AppLockerPolicy -Effective`. If the mode is not `Enabled`, record a failure.

### Result

- **Pass** -- Framework is active, enforcing, and (on Linux) the unauthorized execution test was blocked.
- **Fail** -- Any of the checks above failed.

## Example config

```yaml
capabilities:
  app_allowlisting:
    schedule: "daily"
    params:
      framework: "auto"
```

Force fapolicyd on a system that has multiple frameworks:

```yaml
capabilities:
  app_allowlisting:
    schedule: "daily"
    params:
      framework: "fapolicyd"
```
