
# Change Approval

The change approval workflow enforces a two-person rule for config changes. When enabled, `vigogo config publish` no longer triggers an immediate config reload. Instead, the change is submitted for approval and a second user must approve it before the server reloads.

## Why

In regulated environments or teams with production safety requirements, config changes should be reviewed before taking effect. The approval workflow prevents a single operator from pushing and activating a config change without peer review.

## Configuration

Add the `publish` section to `server.yaml`:

```yaml
publish:
  approval_required: true     # require two-person approval (default: false)
  approval_timeout: "24h"     # how long pending changes stay valid (default: "24h")
```

Authentication must be enabled (`auth.method: basic`, `oidc`, or `isowebauth`) for the two-person rule to work. Without authentication, all requests appear as "anonymous" and the same-user check cannot distinguish approvers.

## CLI Workflow

### 1. Publish (creates a pending change)

When `approval_required` is true, publishing files to `.live/` proceeds normally, but the server reload is blocked. The CLI detects this and automatically submits a pending change request:

```bash
$ vigogo config publish
  (changed)    modules/nginx.vgo
  (ok)         envoys/web.vgo

Published: 12 modules, 3 roles, 8 envoy entries (15 files synced)

Approval required — change 42 submitted (expires 2026-03-17 14:30).
A different user must approve it:
  vigogo config approve 42
```

### 2. List pending changes

```bash
$ vigogo config pending
ID  STATUS   REQUESTED BY  REQUESTED AT       EXPIRES AT
42  pending  alice         2026-03-16 14:30   2026-03-17 14:30
```

### 3. Approve

A different user approves the change, which triggers the config reload:

```bash
$ vigogo config approve 42
Change 42 approved by bob (requested by alice) — config reloaded.
```

### 4. Reject (optional)

To reject a change with an optional reason:


---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
```bash
$ vigogo config reject 42 --reason "nginx worker count too high for staging"
Change 42 rejected.
```

## Two-Person Rule

The approver must be a different user than the requester. If the same user tries to approve their own change, the server returns 403:

```
$ vigogo config approve 42
Error: server returned 403: approver must be a different user than the requester
```

## REST API

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/config/changes` | Create a pending change |
| `GET` | `/api/v1/config/changes` | List pending changes |
| `GET` | `/api/v1/config/changes/{id}` | Get a single change |
| `POST` | `/api/v1/config/changes/{id}/approve` | Approve and reload |
| `POST` | `/api/v1/config/changes/{id}/reject` | Reject with optional reason |

### Create a change

```bash
curl -X POST https://localhost:8443/api/v1/config/changes \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"diff_summary": "updated nginx worker count"}'
```

Response (201):

```json
{
  "id": 42,
  "status": "pending",
  "requested_by": "alice",
  "requested_at": "2026-03-16T14:30:00Z",
  "expires_at": "2026-03-17T14:30:00Z",
  "diff_summary": "updated nginx worker count"
}
```

### Approve

```bash
curl -X POST https://localhost:8443/api/v1/config/changes/42/approve \
  -H "Authorization: Bearer $TOKEN"
```

### Reject

```bash
curl -X POST https://localhost:8443/api/v1/config/changes/42/reject \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"reason": "not ready for production"}'
```

## Expiry

Pending changes that are not approved or rejected within the `approval_timeout` window are automatically expired. A background loop runs every minute to check for expired changes. The default timeout is 24 hours.

When a new change is submitted, any existing pending changes are automatically cancelled (only one pending change at a time).

## When Approval Is Disabled

When `approval_required` is false (the default), `vigogo config publish` triggers an immediate config reload as before. The approval endpoints are still registered but creating changes is optional.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
