
# Password Policy Testing

| | |
|---|---|
| **Machine name** | `password_policy` |
| **Controls** | NIST-800-63B, PCI-DSS-8.3 |
| **Platforms** | Linux, Windows |
| **Timeout** | 5m (default) |

## What it verifies

Verifies that the operating system enforces password quality requirements -- minimum length, character complexity classes, and active rejection of weak passwords. On Linux, it inspects PAM configuration and the `pwquality.conf` settings. On Windows, it queries `net accounts` and the local security policy via `secedit`.

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| *(none)* | | | This capability has no configurable parameters. |

## Thresholds

| Threshold | Type | Default | Description |
|-----------|------|---------|-------------|
| `min_length` | integer | `12` | Minimum required password length. The configured `minlen` (Linux) or minimum password length (Windows) must be at least this value. |
| `require_complexity` | boolean | `true` | Whether character complexity rules must be enforced. |

## Verification logic

### Linux

1. **Parse `/etc/security/pwquality.conf`** -- reads `minlen`, `dcredit`, `ucredit`, `lcredit`, `ocredit`, and `minclass` values. Records all values as evidence. If the file is missing, the check fails.
2. **Check minimum length** -- if `minlen` is unset or below the `min_length` threshold, records an issue.
3. **Check complexity** (when `require_complexity` is true) -- complexity passes if either:
   - `minclass >= 3`, or
   - All three of `dcredit`, `ucredit`, and `lcredit` are negative (requiring at least N characters of each type).
   If neither condition is met, records an issue.
4. **Scan PAM configuration** -- searches `/etc/pam.d/common-password`, `/etc/pam.d/system-auth`, and `/etc/pam.d/password-auth` for `pam_pwquality.so` or `pam_cracklib.so`. If neither module is configured, records an issue.
5. **Test weak password rejection** (optional) -- if `pwscore` is installed, pipes the string `weak` to it. If `pwscore` accepts the password (returns a score rather than rejecting), records an issue.

### Windows

1. **Query `net accounts`** -- parses minimum password length, lockout threshold, maximum password age, and minimum password age. If minimum length is below the `min_length` threshold, records an issue. If the lockout threshold is 0 (disabled), records an issue.
2. **Check complexity requirement** (when `require_complexity` is true) -- exports the local security policy via `secedit` and checks the `PasswordComplexity` setting. If complexity is not enabled (`= 1`), records an issue.

### Result status

- **Pass** -- all checks pass with no issues.
- **Fail** -- one or more issues found.

## Example config

```yaml
capabilities:
  password_policy:
    enabled: true
    thresholds:
      min_length: 14
      require_complexity: true
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
