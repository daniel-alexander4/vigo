
# Sandgorgon API Reference

All endpoints require authentication (API token or session). Admin role required for write operations (commission, decommission, import). Viewer role sufficient for read operations.

Base path: `/api/v1/sandgorgon`

## Assets

### GET /api/v1/sandgorgon/assets

List all managed hardware assets.

**Response** `200 OK`

```json
{
  "assets": [
    {
      "id": 1,
      "bmc_address": "10.0.1.101",
      "hostname": "web01.example.com",
      "serial_number": "SN-2024-00451",
      "model": "Dell PowerEdge R660",
      "asset_tag": "A-00451",
      "rack_location": "DC1-R04-U12",
      "state": "commissioned",
      "envoy_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "created_at": "2025-11-01T10:00:00Z",
      "updated_at": "2026-03-15T14:30:00Z"
    }
  ]
}
```

### GET /api/v1/sandgorgon/assets/{id}

Get a single asset by ID.

**Response** `200 OK` -- same shape as a single element of the `assets` array above.

**Response** `404 Not Found` -- asset does not exist.

## Commission

### POST /api/v1/sandgorgon/commission

Register a machine's BMC with Sandgorgon.

**Request body:**

```json
{
  "bmc_address": "10.0.1.101",
  "hostname": "web01.example.com",
  "serial_number": "SN-2024-00451",
  "model": "Dell PowerEdge R660",
  "asset_tag": "A-00451",
  "rack_location": "DC1-R04-U12",
  "bmc_username": "admin",
  "bmc_password": "secret:sandgorgon/bmc/default"
}
```

Only `bmc_address` is required. All other fields are optional.

If `hostname` matches an enrolled envoy, the asset is linked and its state is set to `commissioned`. Otherwise, the state is `available`.

**Response** `201 Created`

```json
{
  "id": 1,
  "bmc_address": "10.0.1.101",
  "hostname": "web01.example.com",
  "state": "commissioned"
}
```

## Decommission

### POST /api/v1/sandgorgon/decommission/{id}

Trigger a decommission workflow for the asset with the given ID: envoy cert revocation, disk sanitization, verification, and NIST 800-88 certificate generation.

**Request body:**

```json
{
  "technician": "Jane Doe",
  "method": "purge"
}
```

`method` is the NIST 800-88 sanitization category: `clear`, `purge`, or `destroy`. Defaults to `purge`. `technician` is optional but recommended for audit trail.

**Response** `202 Accepted`

```json
{
  "asset_id": 1,
  "state": "decommissioning",
  "certificate_id": 7
}
```

The response returns immediately. Sanitization runs asynchronously. Poll the asset or certificate endpoint for completion.

**Response** `404 Not Found` -- asset does not exist.

**Response** `409 Conflict` -- asset is already decommissioning or retired.

## Certificates

### GET /api/v1/sandgorgon/certificates

List all NIST 800-88 certificates.

**Response** `200 OK`

```json
{
  "certificates": [
    {
      "id": 7,
      "asset_id": 1,
      "hostname": "web01.example.com",
      "serial_number": "SN-2024-00451",
      "model": "Dell PowerEdge R660",
      "method": "purge",
      "technician": "Jane Doe",
      "drives": [
        {
          "device": "/dev/nvme0n1",
          "media_type": "nvme",
          "capacity_gb": 960,
          "sanitization_method": "NVMe Sanitize Block Erase",
          "verification_method": "NVMe Sanitize Log",
          "verification_result": "pass",
          "serial_number": "S5HBNA0T123456"
        }
      ],
      "completed_at": "2026-03-15T14:30:00Z",
      "status": "complete"
    }
  ]
}
```

### GET /api/v1/sandgorgon/certificates/{id}

Get a single certificate by ID. Returns JSON.

**Response** `200 OK` -- same shape as a single element of the `certificates` array above.

**Response** `404 Not Found` -- certificate does not exist.

### GET /api/v1/sandgorgon/certificates/{id}.html

Get a single certificate rendered as an HTML document suitable for printing or archival. Returns `Content-Type: text/html`.

**Response** `200 OK` -- rendered HTML certificate.

**Response** `404 Not Found` -- certificate does not exist.

## Import

### POST /api/v1/sandgorgon/import

Bulk import hardware assets from a CSV file. Multipart file upload.

**Request:** `Content-Type: multipart/form-data`

| Field | Required | Description |
|-------|----------|-------------|
| `source` | Yes | Import source type: `csv` |
| `file` | Yes | The CSV file to import |

Example with curl:

```bash
curl -X POST https://vigo.example.com:8443/api/v1/sandgorgon/import \
  -H "Authorization: Bearer vgot_..." \
  -F "source=csv" \
  -F "file=@/srv/vigo/assets.csv"
```

**Response** `200 OK`

```json
{
  "imported": 5,
  "linked": 3,
  "available": 2,
  "failed": []
}
```

`linked` is the count of assets whose hostname matched an enrolled envoy. `available` is the count that did not match and were created in `available` state.

See [Import](import.md) for the CSV format specification.

## Events

### GET /api/v1/sandgorgon/events/{assetID}

Get lifecycle events for a specific asset.

**Response** `200 OK`

```json
{
  "events": [
    {
      "id": 12,
      "asset_id": 1,
      "event_type": "decommissioned",
      "details": "Technician: Jane Doe, Method: purge",
      "timestamp": "2026-03-15T14:30:00Z"
    },
    {
      "id": 11,
      "asset_id": 1,
      "event_type": "sanitization_done",
      "details": "All drives verified",
      "timestamp": "2026-03-15T14:29:45Z"
    }
  ]
}
```

**Response** `404 Not Found` -- asset does not exist.

---
*Confidential -- Alexander4, LLC. Not for redistribution.*
