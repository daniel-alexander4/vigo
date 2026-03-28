
# SNMP Compliance Collection

| | |
|---|---|
| **Machine name** | `snmp_collector` |
| **Controls** | *(monitoring -- no specific control mapping)* |
| **Platforms** | All |
| **Timeout** | default (5m) |

## What it verifies

Polls one or more network devices via SNMPv2c to collect operational data and verify that the devices are reachable and responding. This is a monitoring capability rather than a compliance assertion -- it does not map to a specific regulatory control. Instead, it gathers evidence (device descriptions, interface counters, custom OIDs) that other capabilities or external processes can use for compliance reporting.

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `targets` | array of objects | *(required)* | List of SNMP targets to poll. The capability is skipped if this is empty or not set. Each target object supports the fields below. |

### Target object fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `host` | string | `"127.0.0.1"` | Hostname or IP address of the SNMP agent. |
| `port` | integer | `161` | UDP port of the SNMP agent. |
| `community` | string | `"public"` | SNMPv2c community string. |
| `label` | string | value of `host` | Human-readable label for this target, used in evidence keys and summaries. |
| `oids` | array of strings | `["1.3.6.1.2.1.1.1.0"]` | OIDs to query via SNMP GET. The default is `sysDescr.0`. |

## Thresholds

This capability has no configurable thresholds.

## Verification logic

1. **Parse targets** -- Read the `targets` array from params. If empty, the capability is skipped.

2. **Poll each target** -- For each target, iterate over its configured OIDs and issue an SNMPv2c GET request:
   - Create a UDP-based SNMP session to `host:port` with the configured `community` string and a 5-second timeout.
   - Parse the OID string into a numeric sequence (e.g., `"1.3.6.1.2.1.1.1.0"` becomes `[1, 3, 6, 1, 2, 1, 1, 1, 0]`).
   - Send the GET request and extract the first varbind value from the response.
   - If any OID query fails for a target, that target is marked as failed.

3. **Record evidence** -- For each target, record the host, label, per-OID results (value or error), and overall reachability status.

### Result

- **Pass** -- All targets responded successfully for all configured OIDs.
- **Warn** -- Some targets failed but at least one succeeded.
- **Fail** -- All targets failed.
- **Skipped** -- No targets configured.

## Example config

```yaml
capabilities:
  snmp_collector:
    schedule: "hourly"
    params:
      targets:
        - label: "core-switch"
          host: "10.0.0.1"
          community: "readonly"
          oids:
            - "1.3.6.1.2.1.1.1.0"     # sysDescr
            - "1.3.6.1.2.1.1.3.0"     # sysUpTime
            - "1.3.6.1.2.1.2.1.0"     # ifNumber
        - label: "edge-firewall"
          host: "10.0.0.254"
          port: 161
          community: "readonly"
          oids:
            - "1.3.6.1.2.1.1.1.0"
```

Minimal single-target configuration using defaults:

```yaml
capabilities:
  snmp_collector:
    schedule: "daily"
    params:
      targets:
        - host: "192.168.1.1"
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
