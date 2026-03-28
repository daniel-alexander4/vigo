
# Privilege Escalation Audit

| | |
|---|---|
| **Machine name** | `privilege_escalation` |
| **Controls** | NIST-AC-6, CIS-6.1 |
| **Platforms** | Linux, Windows |
| **Timeout** | 10m |

## What it verifies

Scans for common privilege escalation vectors -- SUID binaries, file capabilities, world-writable system files, unquoted service paths, and overly permissive administrator group membership. The goal is to detect misconfigurations that an attacker could exploit to elevate from a regular user to root or SYSTEM.

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `suid_allowlist` | array of strings | *(see below)* | Linux only. List of absolute paths to SUID binaries that are expected and acceptable. Any SUID binary not in this list is flagged. |

The default SUID allowlist covers standard system binaries:

```
/usr/bin/sudo, /usr/bin/su, /usr/bin/passwd, /usr/bin/chsh,
/usr/bin/chfn, /usr/bin/newgrp, /usr/bin/gpasswd, /usr/bin/mount,
/usr/bin/umount, /usr/bin/fusermount, /usr/bin/fusermount3,
/usr/bin/pkexec, /usr/bin/crontab, /usr/bin/at, /usr/bin/ssh-agent,
/usr/bin/wall, /usr/bin/expiry, /usr/bin/chage,
/usr/sbin/pam_timestamp_check, /usr/sbin/unix_chkpwd,
/usr/lib/polkit-1/polkit-agent-helper-1,
/usr/lib/dbus-1.0/dbus-daemon-launch-helper,
/usr/libexec/polkit-agent-helper-1,
/usr/libexec/dbus-daemon-launch-helper
```

## Thresholds

| Threshold | Type | Default | Description |
|-----------|------|---------|-------------|
| *(none)* | | | This capability has no configurable thresholds. |

## Verification logic

### Linux

1. **Find SUID binaries** -- runs `find / -perm -4000 -type f` with a 120-second timeout to locate all set-UID binaries on the filesystem. Compares each result against the `suid_allowlist`. Any binary not in the allowlist is flagged as unexpected.
2. **Check file capabilities** -- runs `getcap -r /` with a 60-second timeout to enumerate all files with Linux capabilities. Filters out standard expected entries (`/usr/bin/ping`, `/usr/bin/traceroute`, `/usr/bin/mtr-packet`, `/usr/sbin/clockdiff`). Any remaining entries are flagged as unexpected.
3. **Check world-writable system files** -- runs `find /usr /etc -type f -perm -o+w` with a 30-second timeout. Any world-writable files in `/usr` or `/etc` are flagged (evidence includes up to 20 file paths).

### Windows

1. **Check unquoted service paths** -- queries all Windows services via WMI and identifies those with unquoted paths containing spaces. Unquoted service paths are a classic privilege escalation vector. Evidence includes up to 10 affected service names.
2. **Check local Administrators group** -- runs `net localgroup Administrators` and parses the member list. If more than 3 accounts are in the Administrators group, records an issue recommending review.
3. **Check AlwaysInstallElevated** -- queries the registry key `HKLM\SOFTWARE\Policies\Microsoft\Windows\Installer\AlwaysInstallElevated`. If set to `0x1`, records an issue -- this allows any user to install MSI packages with SYSTEM privileges.

### Result status

- **Pass** -- no privilege escalation vectors found.
- **Warn** -- one or more issues found (SUID binaries outside allowlist, unexpected file capabilities, world-writable system files, unquoted service paths, excessive admin accounts, or AlwaysInstallElevated).

## Example config

```yaml
capabilities:
  privilege_escalation:
    enabled: true
    params:
      suid_allowlist:
        - /usr/bin/sudo
        - /usr/bin/su
        - /usr/bin/passwd
        - /usr/bin/mount
        - /usr/bin/umount
        - /usr/bin/crontab
        - /opt/myapp/bin/helper   # custom app needs SUID
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
