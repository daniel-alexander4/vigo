
---
title: Integrations
---

# Integrations

Vigo forwards events to external platforms for alerting, ticketing, log aggregation, and asset management. Each integration is configured in `server.yaml` under the `integrations:` section and dispatches events asynchronously — no impact on the check-in hot path.

All API keys and credentials support the `secret:` prefix for secure storage via the secrets provider.

## Alerting

### Slack

Sends formatted messages with color-coded severity to a Slack channel.

```yaml
integrations:
  slack:
    enabled: true
    webhook_url: "https://hooks.slack.com/services/T.../B.../xxx"
    events: ["drift.detected", "compliance.drift", "run.failure"]
```

Setup: Create an incoming webhook in your Slack workspace settings and paste the URL.

### PagerDuty

Triggers incidents via PagerDuty Events API v2 with automatic severity mapping.

```yaml
integrations:
  pagerduty:
    enabled: true
    routing_key: "secret:vigo/pagerduty/routing-key"
    events: ["security.rootkit", "security.malware", "run.failure"]
```

| Vigo Event | PagerDuty Severity |
|---|---|
| `security.rootkit`, `security.malware`, `security.integrity` | critical |
| `run.failure`, `security.cve_critical` | error |
| `drift.detected`, `envoy.stale`, `compliance.drift` | warning |
| All others | info |

Setup: Create a service in PagerDuty with Events API v2 integration, copy the routing key.

### Opsgenie

Creates alerts via Opsgenie REST API with priority mapping.

```yaml
integrations:
  opsgenie:
    enabled: true
    api_key: "secret:vigo/opsgenie/api-key"
    events: ["security.rootkit", "security.malware", "compliance.threshold"]
```

| Vigo Event | Opsgenie Priority |
|---|---|
| `security.rootkit`, `security.malware` | P1 |
| `run.failure`, `security.cve_critical` | P2 |
| All others | P3 |

### Microsoft Teams

Sends adaptive cards to a Teams channel via incoming webhook.

```yaml
integrations:
  teams:
    enabled: true
    webhook_url: "https://outlook.office.com/webhook/..."
    events: ["drift.detected", "run.failure"]
```

Setup: Add an incoming webhook connector to your Teams channel and paste the URL.

## PSA / RMM (Ticketing)

### ConnectWise Manage

Creates service tickets when drift or failures are detected. Designed for MSPs managing client infrastructure.

```yaml
integrations:
  connectwise:
    enabled: true
    url: "https://api-na.myconnectwise.net/v4_6_release/apis/3.0"
    company_id: "mycompany"
    public_key: "publickey"
    private_key: "secret:vigo/connectwise/private-key"
    board_id: 1
    events: ["drift.detected", "run.failure", "envoy.stale"]
```

Setup: Create an API member in ConnectWise Manage, note the public/private key pair, and set the board ID for where tickets should land.

### Autotask / Datto PSA

Creates tickets in Autotask PSA.

```yaml
integrations:
  autotask:
    enabled: true
    url: "https://webservices.autotask.net/ATServicesRest/v1.0"
    username: "apiuser"
    password: "secret:vigo/autotask/password"
    queue_id: 8
    events: ["drift.detected", "run.failure"]
```

## SIEM (Log Aggregation)

### Splunk

Forwards events to Splunk HTTP Event Collector (HEC).

```yaml
integrations:
  splunk:
    enabled: true
    url: "https://splunk.example.com:8088"
    token: "secret:vigo/splunk/hec-token"
    index: "vigo"
    source: "vigo"
    events: []    # empty = forward all events
```

Events arrive in Splunk as `sourcetype=vigo:event` with structured JSON including event name, hostname, envoy ID, summary, and details.

### Elasticsearch

Indexes events directly into Elasticsearch.

```yaml
integrations:
  elastic:
    enabled: true
    url: "https://elasticsearch.example.com:9200"
    index: "vigo-events"
    api_key: "secret:vigo/elastic/api-key"
    events: []
```

Documents include `@timestamp`, `event`, `hostname`, `envoy_id`, `summary`, and `details` fields.

### Datadog

Sends events to Datadog with automatic alert type mapping.

```yaml
integrations:
  datadog:
    enabled: true
    api_key: "secret:vigo/datadog/api-key"
    site: "datadoghq.com"    # or "datadoghq.eu"
    events: []
```

Events are tagged with `source:vigo` and `event:<event_name>`.

## CMDB (Asset Management)

### ServiceNow

Creates incidents on security/drift events and can sync envoy inventory as Configuration Items.

```yaml
integrations:
  servicenow:
    enabled: true
    instance: "mycompany.service-now.com"
    username: "vigo-integration"
    password: "secret:vigo/servicenow/password"
    events: ["drift.detected", "run.failure", "security.rootkit"]
```

## Event Types

All integrations can subscribe to any combination of these event types. An empty `events` list forwards all events.

| Event | Trigger |
|---|---|
| `envoy.enrolled` | New envoy enrolled |
| `run.success` | Successful convergence run |
| `run.failure` | Failed convergence run |
| `drift.detected` | Configuration drift corrected |
| `compliance.drift` | Drift affecting compliance controls (framework-aware) |
| `compliance.threshold` | Fleet compliance drops below configured threshold |
| `envoy.stale` | Envoy hasn't checked in within expected interval |
| `secret.rotated` | Secret value rotated in secrets provider |
| `config.reload.failure` | Config publish/reload failed |
| `security.rootkit` | Rootkit detected (chkrootkit/rkhunter) |
| `security.malware` | Malware detected (ClamAV) |
| `security.integrity` | File integrity breach (AIDE) |
| `security.cve_critical` | New critical CVE found (Trivy) |
| `security.hardening_drop` | Lynis hardening score dropped significantly |
