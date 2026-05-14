
# certificate_windows

Manages certificates in Windows certificate stores idempotently via PowerShell. Imports PFX/CER/CRT files, verifies certificate presence by thumbprint, and removes certificates.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `thumbprint` | Conditional | -- | Certificate thumbprint (SHA-1 hash). Required for verification-only checks and for `state=absent`. |
| `source_path` | Conditional | -- | Path to certificate file (`.pfx`, `.p12`, `.cer`, `.crt`). Required for import operations. |
| `password` | No | -- | PFX/P12 password. Only used with `.pfx`/`.p12` files. Use `secret:` prefix. |
| `store` | No | `Cert:\LocalMachine\My` | Certificate store location. |
| `state` | No | `present` | `present` to import/verify, `absent` to remove. |
| `exportable` | No | `false` | Set to `true` to make the private key exportable on import (PFX only). |

At least one of `thumbprint` or `source_path` must be provided. When `source_path` is provided without `thumbprint`, the certificate is imported unconditionally. When both are provided, the thumbprint is checked first to avoid duplicate imports.

## States

- **`present`** -- Ensure the certificate exists in the store. Import from `source_path` if needed.
- **`absent`** -- Ensure the certificate is not in the store. Requires `thumbprint`.

## Common Store Locations

| Store | Description |
|-------|-------------|
| `Cert:\LocalMachine\My` | Personal certificates (default). Used for IIS HTTPS bindings. |
| `Cert:\LocalMachine\Root` | Trusted Root Certification Authorities. |
| `Cert:\LocalMachine\CA` | Intermediate Certification Authorities. |
| `Cert:\CurrentUser\My` | Current user's personal certificates. |

## Idempotency

The executor queries the certificate store via `Get-ChildItem` before acting:

1. If `thumbprint` is provided, the store is searched for a matching certificate.
2. If the certificate already exists in the desired store, no action is taken.
3. If `source_path` is provided and the certificate is missing, `Import-PfxCertificate` or `Import-Certificate` is used depending on file extension.
4. For `state=absent`, the certificate is removed only if it exists.

## Examples

### Import a PFX with password

```yaml
resources:
  - name: import-app-cert
    type: certificate_windows
    source_path: "C:\\certs\\app.pfx"
    password: "secret:vigo/certs/app-pfx-password"
    thumbprint: "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2"
```

### Import a CA certificate into the trusted root store

```yaml
resources:
  - name: import-ca-cert
    type: certificate_windows
    source_path: "C:\\certs\\corporate-ca.cer"
    store: "Cert:\\LocalMachine\\Root"
```

### Verify a certificate exists (no source to import)

```yaml
resources:
  - name: verify-ssl-cert
    type: certificate_windows
    thumbprint: "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2"
    store: "Cert:\\LocalMachine\\My"
```

### Remove an expired certificate

```yaml
resources:
  - name: remove-old-cert
    type: certificate_windows
    thumbprint: "DEADBEEF1234567890DEADBEEF1234567890DEAD"
    state: absent
```

### Import an exportable PFX for IIS

```yaml
resources:
  - name: iis-ssl-cert
    type: certificate_windows
    source_path: "C:\\certs\\webserver.pfx"
    password: "secret:vigo/certs/webserver-password"
    thumbprint: "B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3"
    exportable: "true"
```

## Platform

Windows only.

## Notes

- File extension determines the import method: `.pfx` and `.p12` use `Import-PfxCertificate` (supports private keys and passwords). `.cer` and `.crt` use `Import-Certificate` (public key only).
- When both `thumbprint` and `source_path` are provided, the executor checks for the thumbprint first and skips the import if the certificate is already present. This is the recommended pattern for idempotent imports.
- When only `source_path` is provided (no thumbprint), the certificate is imported on every run where the executor detects it as missing. Providing a thumbprint avoids duplicate imports.
- Use the `secret:` prefix for PFX passwords to keep them out of plaintext config.
- The imported certificate's thumbprint is exposed so downstream tooling can bind it (e.g. to an HTTPS listener) via the standard Windows APIs.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
