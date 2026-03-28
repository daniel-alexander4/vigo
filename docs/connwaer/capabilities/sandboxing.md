
# Application Sandboxing

| | |
|---|---|
| **Machine name** | `sandboxing` |
| **Controls** | CE-MW-5 |
| **Platforms** | Linux, Windows |
| **Timeout** | default (5m) |

## What it verifies

Confirms that a mandatory access control (MAC) framework is present, active, and enforcing policy to sandbox applications. On Linux this checks SELinux and/or AppArmor, plus seccomp availability. On Windows this checks Credential Guard, HVCI, WDAC code integrity enforcement, and Defender Application Guard.

## Parameters

This capability has no configurable parameters.

## Thresholds

This capability has no configurable thresholds.

## Verification logic

### Linux

1. **Detect frameworks** -- Check for SELinux (via `/etc/selinux/config` or `getenforce` on PATH) and AppArmor (via `/sys/module/apparmor` or `aa-status` on PATH). Both can be present simultaneously. If neither is detected, the check fails.

2. **SELinux** (if detected):
   - Run `getenforce` and check that the mode is `Enforcing`. Any other mode (Permissive, Disabled) is recorded as an issue.
   - Run `sestatus` and extract the loaded policy name for evidence.

3. **AppArmor** (if detected):
   - Run `aa-status --json` and parse the JSON output.
   - Count profiles in `enforce` mode, profiles in `complain` mode, and `unconfined` processes. All three are recorded as evidence.
   - If zero profiles are in enforce mode, record an issue.
   - Fallback: if `aa-status` is unavailable, read `/sys/module/apparmor/parameters/enabled` and check for `Y`.

4. **seccomp** -- Check for `/proc/sys/kernel/seccomp` or `Seccomp:` in `/proc/self/status`. Recorded as evidence (available/not available) but does not cause pass/fail on its own.

### Windows

1. **Device Guard / Credential Guard** -- Query `Win32_DeviceGuard` via WMI (`Get-CimInstance`). Parse `SecurityServicesRunning` to determine:
   - **Credential Guard** -- value `1` means running. If not running, record an issue.
   - **HVCI** (Hypervisor-Protected Code Integrity) -- value `2` means running. Recorded as evidence.

2. **WDAC enforcement** -- Check `CodeIntegrityPolicyEnforcementStatus` from the same WMI object. Value `2` means enforced. If not `2`, record an issue.

3. **Defender Application Guard** -- Check `Windows-Defender-ApplicationGuard` optional feature state via `Get-WindowsOptionalFeature`. Recorded as evidence (enabled/disabled) but does not cause pass/fail on its own.

### Result

- **Pass** -- At least one MAC framework is active and enforcing (Linux), or WDAC + Credential Guard are both active (Windows).
- **Warn** -- A framework is detected but not fully enforcing (e.g., SELinux in permissive mode, AppArmor with zero enforce profiles).
- **Fail** -- No MAC framework detected at all (Linux), or critical protections missing (Windows).

## Example config

```yaml
capabilities:
  sandboxing:
    schedule: "daily"
```
