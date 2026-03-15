# local_security_policy

Manages local security policies idempotently via the Windows `secedit` tool. Enforces password policies, account lockout settings, and audit policies.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `policy` | Yes | -- | Policy name (see supported policies below). |
| `value` | Yes | -- | Policy value (integer or string depending on policy). |

## Supported Policies

### Password Policies

| Policy | Description | Example Values |
|--------|-------------|----------------|
| `MinimumPasswordLength` | Minimum number of characters | `8`, `12`, `14` |
| `MaximumPasswordAge` | Maximum password age in days | `90`, `60`, `42` |
| `MinimumPasswordAge` | Minimum password age in days | `1`, `7` |
| `PasswordComplexity` | Require complexity | `1` (enabled), `0` (disabled) |
| `PasswordHistorySize` | Number of remembered passwords | `12`, `24` |

### Account Lockout Policies

| Policy | Description | Example Values |
|--------|-------------|----------------|
| `LockoutBadCount` | Failed attempts before lockout | `3`, `5`, `10` |
| `LockoutDuration` | Lockout duration in minutes | `30`, `60` |
| `ResetLockoutCount` | Reset counter after N minutes | `15`, `30` |

### Audit Policies

| Policy | Description | Example Values |
|--------|-------------|----------------|
| `AuditLogonEvents` | Audit logon events | `0` (none), `1` (success), `2` (failure), `3` (both) |
| `AuditObjectAccess` | Audit object access | `0`, `1`, `2`, `3` |
| `AuditPrivilegeUse` | Audit privilege use | `0`, `1`, `2`, `3` |
| `AuditPolicyChange` | Audit policy changes | `0`, `1`, `2`, `3` |
| `AuditAccountManage` | Audit account management | `0`, `1`, `2`, `3` |

## Idempotency

The executor reads the current policy value via `secedit /export` before acting:

1. The current security database is exported to a temp file and parsed as INI.
2. If the current value matches the desired value, no action is taken.
3. If the values differ, a policy template is written and applied via `secedit /configure`.

## Examples

### Enforce minimum password length

```yaml
resources:
  - name: min-password-length
    type: local_security_policy
    policy: MinimumPasswordLength
    value: "12"
```

### Configure account lockout

```yaml
resources:
  - name: lockout-threshold
    type: local_security_policy
    policy: LockoutBadCount
    value: "5"

  - name: lockout-duration
    type: local_security_policy
    policy: LockoutDuration
    value: "30"

  - name: lockout-reset
    type: local_security_policy
    policy: ResetLockoutCount
    value: "15"
```

### Enable full audit logging

```yaml
resources:
  - name: audit-logon
    type: local_security_policy
    policy: AuditLogonEvents
    value: "3"

  - name: audit-policy-change
    type: local_security_policy
    policy: AuditPolicyChange
    value: "3"

  - name: audit-account-mgmt
    type: local_security_policy
    policy: AuditAccountManage
    value: "3"
```

### CIS-aligned password policy

```yaml
resources:
  - name: password-length
    type: local_security_policy
    policy: MinimumPasswordLength
    value: "14"

  - name: password-age
    type: local_security_policy
    policy: MaximumPasswordAge
    value: "60"

  - name: password-min-age
    type: local_security_policy
    policy: MinimumPasswordAge
    value: "1"

  - name: password-complexity
    type: local_security_policy
    policy: PasswordComplexity
    value: "1"

  - name: password-history
    type: local_security_policy
    policy: PasswordHistorySize
    value: "24"
```

## Platform

Windows only. Requires administrator privileges for `secedit /configure`.

## Notes

- Policy names are case-sensitive and must match exactly as listed above.
- The `secedit` tool writes to the local security database. Group Policy can override these settings on domain-joined machines.
- Audit policy values: `0` = no auditing, `1` = success only, `2` = failure only, `3` = both success and failure.
- Changes take effect immediately after `secedit /configure` completes. No reboot is required.
- Temporary files created during export and configure are cleaned up automatically.
