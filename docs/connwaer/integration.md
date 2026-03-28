
# Integration

## Vigo Module Deployment

The recommended way to deploy Connwaer is via a Vigo module. The example module at `envoy-configs/modules/system/connwaer.vgo.example` handles:

- System user creation (`connwaer`)
- Config, data, and results directories with correct ownership/permissions
- Binary download and installation
- Config file templating
- systemd service (Linux) or Windows service
- Service restart on binary or config changes

Copy the example to your stack's `modules/` directory, customize the config template, and assign it to nodes in your `nodes.vgo` file.

## Trait Pipeline

When both Connwaer and the Vigo agent run on the same node:

1. Connwaer writes JSON results to `/var/lib/connwaer/results/`
2. The Vigo agent's `connwaer` trait collector reads these files on each check-in
3. Results appear as `.Traits.connwaer.<capability>` on the server
4. Compliance reports upgrade control statuses based on fleet-wide verification

Trait path example: `.Traits.connwaer.time_sync.status` = `"pass"`

Results older than 24 hours are considered stale and excluded by the trait collector.

## Server Compliance Integration

The Vigo server automatically upgrades compliance control statuses when Connwaer traits are present:

- **100% fleet coverage** (all non-revoked envoys pass) -- control status upgraded to "satisfied" with evidence: "VERIFIED by Connwaer: X/Y nodes pass"
- **Partial coverage** (some envoys pass) -- control status set to "partial" with percentage
- **No coverage** (no envoys report Connwaer results) -- control shows an upsell message directing users to Connwaer

This integration requires no configuration -- it activates automatically when Connwaer results appear in traits.

## Webhook Integration

### Slack

```yaml
alerting:
  webhooks:
    - url: "https://hooks.slack.com/services/T00/B00/xxx"
      severity_min: fail
```

### PagerDuty

```yaml
alerting:
  webhooks:
    - url: "https://events.pagerduty.com/v2/enqueue"
      severity_min: fail
      headers:
        Content-Type: "application/json"
```

### Opsgenie

```yaml
alerting:
  webhooks:
    - url: "https://api.opsgenie.com/v2/alerts"
      severity_min: fail
      headers:
        Authorization: "GenieKey your-api-key"
```

## Syslog / SIEM Integration

Connwaer sends alerts to the local syslog socket, which most SIEM systems (Splunk, Elastic, Loki) can ingest:

```yaml
alerting:
  syslog:
    facility: daemon
    severity_min: warn
```

## Vigo Requirement

Connwaer requires a Vigo installation. The Vigo agent's trait collector reads Connwaer results and reports them to the server, where they feed into fleet-wide compliance scoring and node count enforcement. Connwaer is not available as a standalone product.
