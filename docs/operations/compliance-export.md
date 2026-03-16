# Compliance Export

Hobnob provides compliance evidence to external tools via three export endpoints: SIEM, CMDB, and OSCAL. Hobnob provides evidence; compliance tools evaluate it.

## Configuration

Enable export endpoints in `server.yaml`:

```yaml
export:
  siem: true     # default: false
  cmdb: true     # default: false
  oscal: true    # default: false
```

Disabled endpoints are not registered on the HTTP server. Requests to a disabled endpoint return 404.

---

## SIEM Export

### GET /api/v1/export/siem

Returns a JSON array of `SIEMEvent` objects derived from run results. Each event represents one resource action from an agent convergence run.

### Query Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `since` | 24 hours ago | RFC 3339 timestamp. Only events after this time are returned |
| `thrall` | (all) | Filter by hostname |
| `limit` | 1000 | Maximum events to return (max 10000) |

### Response

```json
[
  {
    "timestamp": "2026-03-13T10:05:00Z",
    "hostname": "web-01.example.com",
    "thrall_id": "a1b2c3d4",
    "run_id": "run-5678",
    "module_name": "nginx",
    "resource_name": "nginx.conf",
    "resource_type": "file",
    "action": "updated",
    "changed": true,
    "duration_ms": 42,
    "run_status": "compliant",
    "source": "hobnob"
  }
]
```

### Event Fields

| Field | Type | Description |
|-------|------|-------------|
| `timestamp` | string | RFC 3339 time of the resource action |
| `hostname` | string | Thrall hostname |
| `thrall_id` | string | Thrall UUID |
| `run_id` | string | Convergence run ID |
| `module_name` | string | Module that owns the resource |
| `resource_name` | string | Resource name within the module |
| `resource_type` | string | Resource type (file, package, service, etc.) |
| `action` | string | Action taken (created, updated, removed, noop, failed) |
| `changed` | bool | Whether the resource was modified |
| `error` | string | Error message (omitted when empty) |
| `duration_ms` | int | Time spent on this resource in milliseconds |
| `run_status` | string | Overall run status (compliant, drifted, failing) |
| `source` | string | Always `"hobnob"` |

### Integration Examples

**Splunk HEC:**

```bash
hobnobcli export siem --since 2026-03-13T00:00:00Z --output events.json
curl -k https://splunk:8088/services/collector \
  -H "Authorization: Splunk <token>" \
  -d @events.json
```

**Elasticsearch (Filebeat HTTP JSON input):**

```yaml
filebeat.inputs:
  - type: httpjson
    interval: 5m
    request.url: "http://hobnob-server:8443/api/v1/export/siem?since={{.cursor.timestamp}}"
    cursor:
      timestamp:
        value: "{{.last_event.timestamp}}"
```

**Fluentd (HTTP input):**

```xml
<source>
  @type http
  port 9880
</source>

<match hobnob.**>
  @type elasticsearch
  host elastic-server
  port 9200
  index_name hobnob-events
</match>
```

Poll the SIEM endpoint on an interval and POST results to Fluentd's HTTP input.

### Design Decisions

- **No desired/actual state.** Events report what happened (action, changed, error), not what the desired or actual resource state was. This keeps events compact and avoids leaking configuration details into SIEM.
- **No packages in v1.** Package audit data is available via `hobnobcli packages` but is not included in SIEM events.

---

## CMDB Export

### GET /api/v1/export/cmdb

Returns a JSON array of `CMDBRecord` objects. Data is read from the FleetIndex (zero DB queries).

### Query Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `status` | (all) | Filter by compliance status: `compliant`, `drifted`, `failing`, `stale` |
| `environment` | (all) | Filter by environment (e.g., `prod`, `staging`) |

### Response

```json
[
  {
    "hostname": "web-01.example.com",
    "ip": "10.0.1.10",
    "thrall_id": "a1b2c3d4",
    "os": "ubuntu",
    "os_version": "24.04",
    "architecture": "x86_64",
    "kernel": "6.8.0-101-generic",
    "cpu_count": 4,
    "memory_total": 8589934592,
    "compliance_status": "compliant",
    "last_seen": "2026-03-13T10:05:00Z",
    "environment": "prod",
    "enrolled_at": "2026-01-15T08:00:00Z",
    "uptime": 3628800
  }
]
```

### Record Fields

| Field | Type | Description |
|-------|------|-------------|
| `hostname` | string | Thrall hostname |
| `ip` | string | IP address |
| `thrall_id` | string | Thrall UUID |
| `os` | string | OS family (e.g., `ubuntu`, `centos`, `windows`) |
| `os_version` | string | OS version |
| `architecture` | string | CPU architecture (e.g., `x86_64`, `aarch64`) |
| `kernel` | string | Kernel version |
| `cpu_count` | int | Number of CPUs |
| `memory_total` | int | Total memory in bytes |
| `compliance_status` | string | Current compliance status |
| `last_seen` | string | RFC 3339 time of last check-in |
| `environment` | string | Environment tier |
| `enrolled_at` | string | RFC 3339 enrollment time |
| `uptime` | int | System uptime in seconds |

### Trait Mapping

CMDB fields are derived from agent traits:

| CMDB Field | Trait Path |
|------------|------------|
| `os` | `os.family` |
| `os_version` | `os.version` |
| `architecture` | `hardware.architecture` |
| `kernel` | `kernel.version` |
| `cpu_count` | `hardware.cpu_count` |
| `memory_total` | `memory.total` |
| `uptime` | `uptime.seconds` |

### Integration Examples

**ServiceNow CMDB API:**

```bash
hobnobcli export cmdb --status compliant --output cmdb.json
# Transform and POST each record to ServiceNow Table API
curl -X POST "https://instance.service-now.com/api/now/table/cmdb_ci_server" \
  -H "Content-Type: application/json" \
  -H "Authorization: Basic <credentials>" \
  -d '{"name": "web-01.example.com", "os": "ubuntu", ...}'
```

**Generic REST-to-CMDB:**

Poll the endpoint on a schedule and reconcile records against your CMDB. The `thrall_id` field serves as a stable identifier for upsert operations.

### Design Decisions

- **FleetIndex only.** CMDB records are built entirely from in-memory FleetIndex data. No database queries are executed, keeping this endpoint fast under load.
- **No packages in v1.** Installed package lists are not included in CMDB records.

---

## OSCAL Export

### GET /api/v1/export/oscal

Returns a minimal valid OSCAL Assessment Results JSON envelope conforming to the OSCAL v1.1.2 specification.

### Response

```json
{
  "assessment-results": {
    "uuid": "...",
    "metadata": {
      "title": "Hobnob Assessment Results",
      "last-modified": "2026-03-13T10:00:00Z",
      "version": "1.0.0",
      "oscal-version": "1.1.2"
    },
    "results": [
      {
        "uuid": "...",
        "title": "Hobnob Compliance Assessment",
        "start": "2026-03-13T10:00:00Z",
        "findings": [],
        "remarks": "Stub: OSCAL findings mapping is not yet implemented. This envelope is valid OSCAL but contains no control findings."
      }
    ]
  }
}
```

The `findings` array is empty in the current implementation. Future versions will map Hobnob compliance data (resource drift, failing resources, convergence status) to OSCAL control findings.

---

## CLI

All export endpoints are accessible via `hobnobcli export`:

```bash
# Export last 24h of SIEM events
hobnobcli export siem

# Export events since a specific time for one thrall
hobnobcli export siem --since 2026-03-13T00:00:00Z --thrall web-01.example.com

# Export CMDB records for production thralls
hobnobcli export cmdb --environment prod

# Export compliant thralls to a file
hobnobcli export cmdb --status compliant --output fleet.json

# Export OSCAL envelope
hobnobcli export oscal --output assessment.json
```

See [CLI export reference](../cli/export.md) for full flag documentation.

---

## Related

- [Prometheus Metrics](metrics.md) -- runtime observability
- [CLI export](../cli/export.md) -- CLI reference for export commands
- [REST API export endpoints](../api/rest.md#export) -- API reference
