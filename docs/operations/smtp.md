# Email Notifications

The Hobnob server can send email notifications for operational events via an SMTP relay. Notifications are disabled when `smtp.host` is empty.

## Configuration

Add an `smtp:` section to `server.yaml`:

```yaml
smtp:
  host: smtp.example.com
  port: 587                            # default
  from: hobnob@example.com
  username: hobnob
  password: "secret:hobnob/smtp/password"
  tls: true                            # STARTTLS (default)
  recipients:
    - ops@example.com
    - oncall@example.com
```

## Event Types

| Event | Description |
|-------|-------------|
| `thrall.enrolled` | A new thrall was enrolled |
| `run.success` | A convergence run succeeded |
| `run.failure` | A convergence run failed |
| `drift.detected` | Drift was detected on a thrall |
| `secret.rotated` | A secret was rotated |
| `config.reload.failure` | Config file reload failed |
| `thrall.stale` | A thrall has gone stale (missed check-ins) |
| `compliance.threshold` | Fleet compliance dropped below the configured percentage |

## Per-Event Overrides

You can enable only specific events and override recipients per event:

```yaml
smtp:
  host: smtp.example.com
  from: hobnob@example.com
  recipients:
    - ops@example.com
  events:
    run.failure:
      recipients:
        - oncall@example.com
        - ops@example.com
    drift.detected: {}                 # enabled with default recipients
    compliance.threshold:
      threshold: 80                    # fire when compliance drops below 80%
    thrall.stale: {}
```

When `events` is omitted or null, all events are enabled with the default recipient list. When `events` is present, only the listed events are enabled.

## Digest Mode

Instead of sending individual emails for every event, you can batch them into periodic summaries:

```yaml
smtp:
  host: smtp.example.com
  from: hobnob@example.com
  recipients:
    - ops@example.com
  digest:
    interval: "1h"                     # send a digest every hour
    events:
      - run.success
      - drift.detected
```

Events listed in `digest.events` are buffered and sent as a single summary email at the configured interval. Events not in the digest list are sent immediately as usual.

The digest email groups events by type and shows counts, making it easy to spot patterns without inbox overload.

## Configuration Reference

| Field | Default | Description |
|-------|---------|-------------|
| `host` | (required) | SMTP relay hostname |
| `port` | 587 | SMTP port |
| `from` | (required) | Sender email address |
| `username` | | SMTP auth username (optional) |
| `password` | | SMTP auth password (supports `secret:` prefix) |
| `tls` | true | Use STARTTLS |
| `recipients` | | Default recipient list |
| `events` | all | Per-event configuration map |
| `digest.interval` | | Batch window (e.g., "1h", "30m"); empty = disabled |
| `digest.events` | | Events routed to digest instead of immediate |

## Verifying SMTP

Use `hobnobcli doctor` to check SMTP connectivity. The doctor endpoint performs a TCP connection test to the configured SMTP server and reports pass/fail.
