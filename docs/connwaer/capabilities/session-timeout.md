
# Session Timeout Verification

| | |
|---|---|
| **Machine name** | `session_timeout` |
| **Controls** | HIPAA-164.312(a)(2)(iii), PCI-DSS-8.1.8 |
| **Platforms** | Linux, Windows |
| **Timeout** | 5m (default) |

## What it verifies

Confirms that idle sessions are automatically terminated or locked within a maximum time limit. Unattended sessions are a common attack vector -- an unlocked terminal or RDP session gives an attacker immediate access at the logged-in user's privilege level.

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| *(none)* | | | This capability has no configurable parameters. |

## Thresholds

| Threshold | Type | Default | Description |
|-----------|------|---------|-------------|
| `max_idle_seconds` | integer | `900` | Maximum acceptable idle timeout in seconds (default is 15 minutes). Both SSH and shell timeouts (Linux) or screen lock and RDP timeouts (Windows) are compared against this value. |

## Verification logic

### Linux

1. **Parse `sshd_config`** -- reads `/etc/ssh/sshd_config` and all `.conf` files in `/etc/ssh/sshd_config.d/` (sorted alphabetically, later values override). Extracts `ClientAliveInterval` and `ClientAliveCountMax`.
2. **Compute effective SSH timeout** -- `ClientAliveInterval * ClientAliveCountMax`. The SSH defaults are `ClientAliveInterval=0` (disabled) and `ClientAliveCountMax=3`. If the interval is 0, the effective timeout is 0 (disabled). If the effective timeout exceeds `max_idle_seconds`, records an issue.
3. **Check TMOUT** -- scans `/etc/profile` and all `.sh` files in `/etc/profile.d/` for `TMOUT=NNN` declarations (handles `export TMOUT=`, `readonly TMOUT=`, and bare `TMOUT=`). If TMOUT is not set anywhere, records an issue. If the TMOUT value exceeds `max_idle_seconds`, records an issue.
4. **Compute overall effective timeout** -- takes the minimum of the SSH effective timeout and TMOUT. If both are disabled (0 / unset), the effective timeout is 0 and the check fails.

### Windows

1. **Check screen lock timeout** -- queries the registry at `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System\InactivityTimeoutSecs`. If not configured, records an issue. If the value exceeds `max_idle_seconds`, records an issue.
2. **Check screensaver lock** -- queries `HKCU\Control Panel\Desktop\ScreenSaverIsSecure`. Records whether the screensaver requires a password on resume (informational evidence).
3. **Check RDP idle disconnect** -- queries `HKLM\SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services\MaxIdleTime` (stored in milliseconds, converted to seconds). If the value exceeds `max_idle_seconds`, records an issue.
4. **Compute overall effective timeout** -- takes the minimum of the screen lock timeout and the RDP idle timeout. If neither is configured, the check fails.

### Result status

- **Pass** -- all session timeout mechanisms are configured within the threshold.
- **Warn** -- at least one timeout mechanism is configured but exceeds the threshold, or one mechanism is missing while another is present.
- **Fail** -- no session timeout is configured at all (effective timeout is 0 on Linux, or neither screen lock nor RDP timeout is set on Windows).

## Example config

```yaml
capabilities:
  session_timeout:
    enabled: true
    thresholds:
      max_idle_seconds: 600   # 10 minutes
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
