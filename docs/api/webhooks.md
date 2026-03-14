# Webhooks

Webhooks deliver event notifications to external HTTP endpoints as they happen. Each delivery is signed with HMAC-SHA256 so the receiver can verify authenticity.

## Registration

Register a webhook via the CLI or REST API:

```bash
hobnobcli webhooks add \
  --url https://ops.example.com/hooks/hobnob \
  --secret "my-signing-secret" \
  --events run.success,run.failure,drift.detected
```

Or via REST:

```bash
curl -X POST http://server:8181/api/v1/webhooks \
  -H 'Content-Type: application/json' \
  -d '{
    "url": "https://ops.example.com/hooks/hobnob",
    "secret": "my-signing-secret",
    "events": ["run.success", "run.failure", "drift.detected"]
  }'
```

## Event Types

| Event | Description |
|-------|-------------|
| `run.success` | A convergence run completed successfully |
| `run.failure` | A convergence run failed |
| `run.partial` | A convergence run partially succeeded |
| `thrall.enrolled` | A new thrall was enrolled |
| `drift.detected` | Drift was detected on a thrall (converged with changes) |
| `secret.rotated` | A secret was rotated, affecting one or more thralls |
| `config.reload.failure` | A config reload failed |
| `thrall.stale` | A thrall has not checked in within the stale threshold |
| `compliance.threshold` | Fleet compliance dropped below the configured threshold |

## Payload Format

All webhook deliveries are HTTP POST requests with a JSON body:

```json
{
  "event": "run.failure",
  "timestamp": "2026-03-13T10:00:00Z",
  "thrall": {
    "id": "abc-123",
    "hostname": "web-01.example.com",
    "ip": "10.0.1.10"
  },
  "run": {
    "id": "run-456",
    "status": "failure",
    "changed": 3,
    "failed": 1
  },
  "details": null
}
```

The `thrall` and `run` fields are present when relevant to the event. The `details` field carries event-specific data (e.g., for `secret.rotated`, it contains the secret path and affected node count).

### Secret Rotation Payload

```json
{
  "event": "secret.rotated",
  "timestamp": "2026-03-13T10:00:00Z",
  "details": {
    "secret_path": "hobnob/db/password",
    "affected_nodes": 5
  }
}
```

## HMAC-SHA256 Signing

Every delivery includes two custom headers:

| Header | Description |
|--------|-------------|
| `X-Hobnob-Signature` | `sha256=<hex-encoded HMAC-SHA256>` computed over the raw JSON body using the webhook's secret |
| `X-Hobnob-Event` | The event name (e.g., `run.failure`) |

To verify a delivery, compute `HMAC-SHA256(secret, body)` and compare with the signature header:

```python
import hmac, hashlib

def verify(secret, body, signature_header):
    expected = "sha256=" + hmac.new(
        secret.encode(), body, hashlib.sha256
    ).hexdigest()
    return hmac.compare_digest(expected, signature_header)
```

## SSRF Protection

Webhook URLs are validated before delivery. The following are rejected:

- Loopback addresses (`127.0.0.0/8`, `::1`, `localhost`)
- Private network addresses (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`)
- Link-local addresses (`169.254.0.0/16`)
- Non-HTTP(S) schemes

DNS names are allowed (resolution happens at delivery time).

## Retry Behavior

Failed deliveries are retried with linear backoff:

| Parameter | Default | Config key |
|-----------|---------|------------|
| Max retries | 3 | `tuning.webhook.max_retries` |
| Retry delay | 5s (multiplied by attempt number) | `tuning.webhook.retry_delay` |
| Request timeout | 10s | `tuning.webhook.timeout` |
| Max concurrent deliveries | 20 | `tuning.webhook.max_concurrent` |

A delivery is considered successful if the endpoint returns HTTP status < 300. After exhausting all retries, the failure is logged but the webhook remains active for future events.

## Management

```bash
# List all webhooks
hobnobcli webhooks list

# Delete a webhook
hobnobcli webhooks delete <webhook-id>
```

Webhooks are disabled in spoke mode. Only the hub (or standalone server) dispatches webhook events.
