
# Kill Switch Validation

| | |
|---|---|
| **Machine name** | `kill_switch` |
| **Controls** | RTS7-Art14-1 |
| **Platforms** | All |
| **Timeout** | default (5m) |

## What it verifies

Confirms that an emergency kill switch mechanism is installed, functional, and able to reach its targets. This validates that a binary capable of shutting down a system or service exists, is executable, passes a dry-run test, and can reach all configured target endpoints. Used primarily for DORA (Digital Operational Resilience Act) compliance, where regulated entities must demonstrate the ability to rapidly halt operations.

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `binary_path` | string | *(required)* | Absolute path to the kill switch binary. The capability is skipped if this is not set. |
| `config_path` | string | *(none)* | Path to the kill switch configuration file. If set, passed to the binary as `--config <path>` during the dry-run, and its existence is verified. |
| `dry_run_flag` | string | `"--dry-run"` | The flag passed to the binary to trigger a non-destructive dry-run. |
| `targets` | array | `[]` | List of target endpoints the kill switch must be able to reach. Each entry can be a string (`"host:port"`) or an object with an `address` field. |

## Thresholds

This capability has no configurable thresholds.

## Verification logic

1. **Binary exists** -- Check that the file at `binary_path` exists on disk. If not found, the check fails immediately.

2. **Binary is executable (Unix only)** -- Verify that the execute bit is set on the binary. If the binary exists but is not executable, record a failure.

3. **Config file exists** -- If `config_path` is set, verify the file exists. A missing config file is recorded as a failure.

4. **Dry-run execution** -- Run the kill switch binary with the configured dry-run flag (and `--config <path>` if `config_path` is set). Capture stdout and stderr. A non-zero exit code is recorded as a failure. The last 5 lines of stdout and first 3 lines of stderr are captured as evidence.

5. **Target reachability** -- For each entry in `targets`, attempt a TCP connection with a 5-second timeout. First tries direct `SocketAddr` parsing; if that fails, falls back to DNS resolution via `ToSocketAddrs`. Unreachable targets are recorded as issues.

### Result

- **Pass** -- Binary exists, is executable, dry-run succeeded, and all targets are reachable.
- **Fail** -- Binary not found, or dry-run failed.
- **Warn** -- Binary and dry-run are fine, but one or more targets are unreachable.
- **Skipped** -- `binary_path` is not configured.

## Example config

```yaml
capabilities:
  kill_switch:
    schedule: "daily"
    params:
      binary_path: "/usr/local/bin/emergency-halt"
      config_path: "/etc/emergency-halt/config.yaml"
      dry_run_flag: "--dry-run"
      targets:
        - "trading-gw-01.internal:8443"
        - "trading-gw-02.internal:8443"
```

Minimal configuration with just the binary:

```yaml
capabilities:
  kill_switch:
    schedule: "daily"
    params:
      binary_path: "/opt/killswitch/halt"
```

Using object-style targets:

```yaml
capabilities:
  kill_switch:
    schedule: "weekly"
    params:
      binary_path: "/usr/local/bin/halt-trading"
      targets:
        - address: "10.0.1.50:443"
        - address: "10.0.1.51:443"
```
