
# certificates

The `certificates` trait collector scans well-known certificate directories for PEM/CRT files and reports their expiry dates and days remaining.

## Scanned Directories

- `/etc/ssl/certs/`
- `/etc/pki/tls/certs/`

Only `.pem` and `.crt` files are scanned. Symlinks are skipped to avoid duplicates.

## Trait Structure

```json
{
  "certificates": {
    "certificates": [
      {
        "path": "/etc/ssl/certs/server.pem",
        "subject": "CN=server.example.com",
        "expires": "2027-01-15T00:00:00Z",
        "days_remaining": 307
      }
    ]
  }
}
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `certificates[].path` | string | Absolute path to the certificate file |
| `certificates[].subject` | string | Certificate subject (CN) |
| `certificates[].expires` | string | RFC 3339 expiry timestamp |
| `certificates[].days_remaining` | int | Days until expiry |

## Platform Support

| Platform | Method |
|----------|--------|
| Linux | `openssl x509 -noout -enddate/-subject` |
| macOS | same |
| FreeBSD | same |
| OpenBSD | same |
| NetBSD | same |
| Windows | Not supported (returns null) |

## Use Cases

Alert on expiring certificates:

```yaml
# In a when expression (via template)
- name: cert-warning
  type: exec
  command: "echo 'Certificate expiring soon' | mail admin@example.com"
  when: "flag_set('cert-check')"
```

## Classification

**Stable** -- certificate expiry dates change infrequently. Cached across convergence cycles.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
