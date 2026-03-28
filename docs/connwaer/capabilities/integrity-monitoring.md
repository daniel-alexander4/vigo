
# File Integrity Monitoring

| | |
|---|---|
| **Machine name** | `integrity_monitoring` |
| **Controls** | PCI-DSS-11.5, NIST-SI-7 |
| **Platforms** | Linux, Windows |
| **Timeout** | 10m |

## What it verifies

Confirms that a file integrity monitoring (FIM) tool is installed and running. On Linux, checks for AIDE, OSSEC, or Wazuh, and optionally runs an AIDE integrity check to detect unauthorized file changes. On Windows, checks for OSSEC/Wazuh services, Tripwire, and the last result from Windows System File Checker (SFC).

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `aide_binary` | string | `"aide"` | Linux only. Path to the AIDE binary. Override if AIDE is installed in a non-standard location. |
| `run_aide_check` | boolean | `true` | Linux only. Whether to execute `aide --check` when an AIDE database exists. Disable if the check is too slow for your environment. |
| `tripwire_binary` | string | `"tripwire.exe"` | Windows only. Path to the Tripwire binary. Used as a fallback if OSSEC/Wazuh are not found. |

## Thresholds

| Threshold | Type | Default | Description |
|-----------|------|---------|-------------|
| `expected_changes_max` | integer | `0` | Linux only. Maximum number of file changes (added + removed + changed) that AIDE may report before the check records an issue. Set to `0` for zero-tolerance. |

## Verification logic

### Linux

1. **Check AIDE** -- runs `aide --version` (or the configured `aide_binary`) to confirm it is installed.
2. **Check AIDE database** -- looks for the AIDE database at `/var/lib/aide/aide.db`, `/var/lib/aide/aide.db.gz`, `/etc/aide/aide.db`, and `/var/lib/aide.db`. If no database is found, records an issue -- AIDE needs `aide --init` before it can detect changes.
3. **Run AIDE check** (when `run_aide_check` is true and the database exists) -- executes `aide --check` and parses the summary output for added, removed, and changed entry counts. If the total exceeds `expected_changes_max`, records an issue.
4. **Check OSSEC/Wazuh** -- looks for `/var/ossec/bin/ossec-control` or `/var/ossec/bin/wazuh-control` and queries their status. Also checks `systemctl is-active wazuh-agent` as a fallback. If found but not running, records an issue.
5. **No FIM tool** -- if neither AIDE, OSSEC, nor Wazuh is found, the check fails.

### Windows

1. **Check OSSEC/Wazuh services** -- queries the `OssecSvc` and `WazuhSvc` services via `sc.exe query`. If found but not running, records an issue.
2. **Check Tripwire** (fallback) -- if neither OSSEC nor Wazuh is found, runs the Tripwire binary with `--version` to check for installation.
3. **Check Windows SFC** -- parses the CBS log (`%SystemRoot%\Logs\CBS\CBS.log`) for the most recent "Verify complete" entry. Records whether the last SFC run found integrity violations. This is informational evidence, not a pass/fail gate.
4. **No FIM tool** -- if neither OSSEC, Wazuh, nor Tripwire is found, the check fails.

### Result status

- **Pass** -- at least one FIM tool is installed, running, and (on Linux) AIDE shows no unexpected changes.
- **Warn** -- a FIM tool is installed but not running, or AIDE detected changes above the threshold.
- **Fail** -- no file integrity monitoring tool is found.

## Example config

```yaml
capabilities:
  integrity_monitoring:
    enabled: true
    params:
      aide_binary: /usr/sbin/aide
      run_aide_check: true
    thresholds:
      expected_changes_max: 0
```
