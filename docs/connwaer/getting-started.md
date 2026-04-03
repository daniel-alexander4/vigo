
# Getting Started

## Installation

### Via Vigo Module (recommended)

If you run Vigo, deploy Connwaer using the included module. Copy `example-configs/modules/system/connwaer.vgo.example` to your stockpile and assign it to nodes that need compliance verification. The module handles the binary, config file, directories, user, and systemd service.

### Manual Installation

Download the Connwaer binary for your platform from your release server and place it at `/usr/local/sbin/connwaer`:

```bash
chmod +x connwaer
sudo mv connwaer /usr/local/sbin/connwaer
```

Create the config directory and a minimal config file:

```bash
sudo mkdir -p /etc/connwaer /var/lib/connwaer/results
```

Create `/etc/connwaer/connwaer.yaml`:

```yaml
log_level: info

capabilities:
  time_sync:
    enabled: true
    schedule: "@every 5m"
    thresholds:
      max_offset_ms: 100
  session_timeout:
    enabled: true
    schedule: "@daily"
    thresholds:
      max_idle_seconds: 900
```

## First Run

Test a single capability interactively:

```bash
connwaer verify time_sync
```

This prints the JSON result to stdout and writes it to the results directory.

Run all enabled capabilities once:

```bash
connwaer once
```

Check the last result per capability:


---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
```bash
connwaer status
```

## Install as a Service

Connwaer can install itself as a systemd service (Linux) or Windows service:

```bash
sudo connwaer install-service
```

This copies the binary to `/usr/local/sbin/connwaer`, writes a systemd unit to `/etc/systemd/system/connwaer.service`, enables and starts the service.

## Verify It Works

After starting the service, wait for the first scheduled run, then check status:

```bash
connwaer status
```

Expected output:

```
time_sync                 pass     2026-03-28T10:00:01Z  NTP offset 12ms (threshold: 100ms)
session_timeout           pass     2026-03-28T10:00:02Z  SSH timeout 300s (threshold: 900s)
```

## Next Steps

- Enable more capabilities in `connwaer.yaml` -- see the [Capability Index](capabilities/_index.md)
- Configure [alerting](alerting.md) for immediate notification on failures
- Set up [integration](integration.md) with Vigo for fleet-wide compliance scoring
- Try the [demo environment](https://github.com/daniel-alexander4/vigo/tree/main/demo/connwaer) for a guided walkthrough
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
