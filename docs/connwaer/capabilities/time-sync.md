
# Time Synchronization

| | |
|---|---|
| **Machine name** | `time_sync` |
| **Controls** | PCI-DSS-10.4, NIST-AU-8 |
| **Platforms** | Linux, Windows |
| **Timeout** | 5m (default) |

## What it verifies

Confirms that an NTP service is running and that the system clock is synchronized within an acceptable offset. Accurate time is essential for audit log integrity, certificate validation, and cross-system event correlation. The measured offset is recorded to the trends database for drift monitoring over time.

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `ntp_server` | string | *(none)* | Optional. An NTP server to query directly for a one-shot offset measurement, independent of whatever NTP service is running locally. On Linux, uses `ntpdate -q`. On Windows, uses `w32tm /stripchart`. |

## Thresholds

| Threshold | Type | Default | Description |
|-----------|------|---------|-------------|
| `max_offset_ms` | float | `100.0` | Maximum acceptable clock offset in milliseconds. If the measured offset exceeds this value, the check fails or warns. |

## Verification logic

### Linux

1. **Detect active NTP service** -- checks `systemctl is-active` for `chronyd`, `ntpd`, `ntp`, and `systemd-timesyncd`, in that order. If none are running, records a failure.
2. **Query offset** -- depending on which service is active:
   - **chronyd** -- runs `chronyc tracking` and parses the `Last offset` line (preferred) or `System time` line. Converts the offset from seconds to milliseconds.
   - **ntpd** -- runs `ntpq -p` and finds the active peer (line starting with `*`). Parses the offset from the 9th column (already in milliseconds).
   - **systemd-timesyncd** -- runs `timedatectl show-timesync` to get the server name, and `timedatectl status` to check whether the clock shows "NTP synchronized: yes". Does not extract a precise offset value.
3. **Compare offset to threshold** -- if the measured offset exceeds `max_offset_ms`, records an issue.
4. **Manual NTP check** (optional) -- if `ntp_server` is set, runs `ntpdate -q <server>` and parses the offset from the output. Recorded as additional evidence.
5. **Record to trends** -- the measured offset in milliseconds is written to the trends database for historical analysis.

### Windows

1. **Check w32time service** -- runs `sc.exe query w32time` and checks whether the service is in the `RUNNING` state. If not, records a failure.
2. **Query sync status** -- runs `w32tm /query /status` and parses the `Source` and `Stratum` fields. If the source is `Free-Running` or `Local CMOS Clock`, records an issue -- the system is not syncing to an external NTP server.
3. **Measure offset** (when `ntp_server` is set) -- runs `w32tm /stripchart /computer:<server> /samples:1 /dataonly` and parses the offset from the last line of output. Converts from seconds to milliseconds and compares against `max_offset_ms`.

### Result status

- **Pass** -- NTP service is running and offset is within threshold.
- **Warn** -- NTP service is running but offset exceeds threshold, or time source is local-only.
- **Fail** -- no NTP service is running (Linux) or w32time is not running (Windows).

## Example config

```yaml
capabilities:
  time_sync:
    enabled: true
    params:
      ntp_server: time.nist.gov
    thresholds:
      max_offset_ms: 50.0
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
