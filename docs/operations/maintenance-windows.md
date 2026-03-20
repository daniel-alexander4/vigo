
# Maintenance Windows

Vigo supports per-envoy maintenance windows and global change freezes to prevent configuration changes during planned outages.

## Per-Envoy Maintenance

During a maintenance window, a envoy receives an empty policy on check-in. It skips convergence but still reports traits, maintaining visibility without making changes.

### CLI

```bash
# Set maintenance until 6 AM tomorrow
vigogo envoys maintenance <id> --until 2026-03-14T06:00:00Z

# Clear maintenance
vigogo envoys maintenance <id> --clear
```

### API

```bash
# Set maintenance
curl -X PUT https://server:8443/api/v1/envoys/<id>/maintenance \
  -d '{"until": "2026-03-14T06:00:00Z"}'

# Clear maintenance
curl -X DELETE https://server:8443/api/v1/envoys/<id>/maintenance
```

Maintenance windows expire automatically when the `until` timestamp passes.

## Global Change Freeze

A global freeze prevents all config publishes. Use this during release windows or incident response.

### CLI

```bash
# Freeze for 24 hours
vigogo config freeze --until 24h

# Freeze until a specific time
vigogo config freeze --until 2026-03-14T06:00:00Z

# Remove freeze
vigogo config unfreeze
```

### API

```bash
# Freeze
curl -X POST https://server:8443/api/v1/config/freeze \
  -d '{"freeze_until": "2026-03-14T06:00:00Z"}'

# Unfreeze
curl -X POST https://server:8443/api/v1/config/unfreeze
```

While frozen, `vigogo config publish` returns an error with the freeze expiry time.
