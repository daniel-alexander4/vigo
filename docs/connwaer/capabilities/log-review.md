
# Automated Log Review

| | |
|---|---|
| **Machine name** | `log_review` |
| **Controls** | PCI-10.4.1 |
| **Platforms** | Linux, Windows |
| **Timeout** | default (5m) |

## What it verifies

Confirms that audit logging infrastructure is active and performs automated analysis of today's log events against anomaly thresholds. On Linux this uses auditd and ausearch to scan for failed logins, privilege escalation, access violations, account changes, and configuration changes. On Windows this queries the Security event log for equivalent event IDs.

## Parameters

This capability has no configurable parameters.

## Thresholds

| Threshold | Type | Default | Description |
|-----------|------|---------|-------------|
| `anomaly_threshold` | integer | (per-rule defaults) | Linux only. Global override for all rule thresholds. When set to a value greater than 0, replaces the individual per-rule thresholds below. |
| `failed_logon_threshold` | integer | `10` | Windows only. Maximum failed logon events (Event ID 4625) in 24 hours before flagging. |
| `account_change_threshold` | integer | `5` | Windows only. Maximum account management events (Event IDs 4720-4726) in 24 hours before flagging. |

## Verification logic

### Linux

1. **auditd service** -- Run `systemctl is-active auditd`. If the service is not `active`, the check fails immediately.

2. **Audit rules** -- Run `auditctl -l` and count loaded rules. If zero rules are loaded, record a failure.

3. **Rule-based analysis** -- Run `ausearch` for each of five built-in rules, counting matching events from today:

   | Rule | ausearch filter | Default threshold | Severity |
   |------|----------------|-------------------|----------|
   | `failed_logins` | `USER_LOGIN` with `--success no` | 10 | warn |
   | `privilege_escalation` | `USER_AUTH`, `USER_ROLE_CHANGE` | 50 | fail |
   | `file_access_violations` | `AVC` (SELinux/AppArmor denials) | 20 | warn |
   | `account_changes` | `ADD_USER`, `DEL_USER`, `ADD_GROUP`, `DEL_GROUP`, `USER_MGMT`, `GRP_MGMT` | 5 | warn |
   | `config_changes` | `MAC_CONFIG_CHANGE`, `DAEMON_CONFIG` | 10 | warn |

   Each rule counts lines starting with `type=` in the ausearch output. If the count exceeds the threshold (or the global `anomaly_threshold` override), the rule is flagged.

4. **Audit log size** -- Check the file size of `/var/log/audit/audit.log`. If it exceeds 500 MB, flag a warning about log rotation.

### Windows

1. **Event Log service** -- Query `EventLog` via `sc.exe query`. If not `RUNNING`, the check fails immediately.

2. **Failed logons** -- Query Security log for Event ID 4625 in the last 24 hours. If the count exceeds `failed_logon_threshold`, flag a warning.

3. **Privilege events** -- Query Security log for Event ID 4672 (special privilege logon) in the last 24 hours. Recorded as evidence.

4. **Account management** -- Query Security log for Event IDs 4720, 4722, 4723, 4724, 4725, 4726 in the last 24 hours. If the count exceeds `account_change_threshold`, flag a warning.

### Result


- **Pass** -- Audit system is active, rules are loaded, and no thresholds are exceeded.
- **Warn** -- Audit system is active but one or more anomaly thresholds were exceeded, or the audit log is very large.
- **Fail** -- auditd/Event Log is not running, or no audit rules are loaded.

## Example config

```yaml
capabilities:
  log_review:
    schedule: "daily"
    thresholds:
      anomaly_threshold: 25
```

Windows with custom thresholds:

```yaml
capabilities:
  log_review:
    schedule: "daily"
    thresholds:
      failed_logon_threshold: 20
      account_change_threshold: 10
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
