
# Alerting

Connwaer can dispatch alerts when capabilities fail or recover. Three backends are supported: webhooks, shell commands, and syslog (Unix only).

## Configuration

```yaml
alerting:
  dedup_window_seconds: 3600     # suppress duplicate alerts for 1 hour
  recovery_alerts: true          # alert when a failing capability passes again

  webhooks:
    - url: "https://hooks.slack.com/services/T00/B00/xxx"
      severity_min: fail
      headers:
        Authorization: "Bearer token123"

    - url: "https://events.pagerduty.com/v2/enqueue"
      severity_min: fail

  commands:
    - command: /usr/local/bin/notify-oncall
      args: ["--channel", "compliance"]
      severity_min: warn

  syslog:
    facility: daemon
    severity_min: warn
```

## Severity Levels

| Level | When | Priority |
|-------|------|----------|
| `warn` | Capability returned `warn` status | 1 |
| `fail` / `error` | Capability returned `fail` or `error` status | 2 |

The `severity_min` field on each backend filters alerts. Setting `severity_min: warn` sends both warnings and failures. Setting `severity_min: fail` sends only failures and errors.

## Deduplication

The `dedup_window_seconds` setting (default: 3600) prevents repeated alerts for the same capability and status. If `time_sync` fails at 10:00 and again at 10:05, only the first alert fires. The dedup window resets when the status changes (e.g., fail -> pass).

## Recovery Alerts

When `recovery_alerts: true`, an alert fires when a previously-failing capability returns to `pass`. This confirms that the issue was resolved.

## Webhook Backend

Sends a JSON POST to the configured URL with retry (3 attempts, exponential backoff starting at 500ms).

Payload:

```json
{
  "severity": "fail",
  "capability": "time_sync",
  "status": "fail",
  "summary": "NTP offset 2450ms exceeds threshold 100ms",
  "hostname": "web-01.example.com",
  "platform": "linux",
  "completed_at": "2026-03-28T10:00:01Z",
  "controls": ["PCI-DSS-10.4", "NIST-AU-8"]
}
```

Headers: `Content-Type: application/json`, `User-Agent: connwaer`, plus any custom headers from the config.

## Command Backend

Executes a shell command. The full JSON result is available as structured input for custom processing.

```yaml
commands:
  - command: /usr/local/bin/compliance-alert
    args: ["--format", "json"]
    severity_min: fail
```

## Syslog Backend (Unix only)

Sends alerts to the local syslog socket (`/dev/log`) via UDP datagram.

```yaml
syslog:
  facility: daemon
  severity_min: warn
```

Syslog priority mapping:

| Capability Status | Syslog Priority |
|-------------------|-----------------|
| `fail` / `error` | daemon.err (11) |
| `warn` | daemon.warning (12) |
| `pass` (recovery) | daemon.info (14) |

Message format: `<priority>connwaer: capability_name severity -- summary`
