
# CLI Reference

## Global Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--config` | `-c` | `/etc/connwaer/connwaer.yaml` | Path to config file |
| `--log-level` | | (from config) | Override log level: `trace`, `debug`, `info`, `warn`, `error` |

Global flags must appear before the subcommand.

## Commands

### run

Start the daemon. Runs the scheduler loop, executing capabilities according to their cron schedules. Handles SIGINT/SIGTERM for graceful shutdown.

```bash
connwaer run
connwaer run --once    # run all enabled capabilities once, then exit
```

The `run` command is the default -- if no subcommand is given, `connwaer` starts in daemon mode.

### once

Alias for `run --once`. Runs all enabled capabilities in dependency order, then exits.

```bash
connwaer once
```

### verify

Run a single capability and print the JSON result to stdout. Also writes the result to the results directory.

```bash
connwaer verify time_sync
connwaer verify capacity_monitor
connwaer -c /path/to/config.yaml verify rng_validation
```

### status

Show the last result per capability by reading JSON files from the results directory.

```bash
connwaer status
```

Output format:

```
time_sync                 pass     2026-03-28T10:00:01Z  NTP offset 12ms (threshold: 100ms)
session_timeout           pass     2026-03-28T10:00:02Z  SSH timeout 300s (threshold: 900s)
capacity_monitor          warn     2026-03-28T10:05:00Z  Disk usage 87% (threshold: 90%)
```

### install-service

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---

Install Connwaer as a system service. Copies the binary to the default install path, creates necessary directories, writes the service unit, and starts the service.

```bash
sudo connwaer install-service
```

On Linux: creates a systemd unit at `/etc/systemd/system/connwaer.service`, runs `systemctl daemon-reload`, enables and starts the service.

On Windows: creates a Windows service named `Connwaer` via `sc.exe`, configures restart-on-failure, and starts the service.

### version

Print the version and exit.

```bash
connwaer version
```

### help

Print usage information.

```bash
connwaer help
connwaer --help
connwaer -h
```

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Error (config load failure, capability error, unknown command) |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
