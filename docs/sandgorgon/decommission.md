# Decommission Workflow

The decommission workflow takes a commissioned asset through sanitization to retirement. It is the core of Sandgorgon Phase 1 and the primary differentiator -- auditable, per-drive sanitization with an honest NIST 800-88 certificate at the end.

## Triggering Decommission

Decommission can be triggered three ways:

- **CLI:** `vigocli sandgorgon decommission <hostname> [--method clear|purge]`
- **REST API:** `POST /api/v1/sandgorgon/decommission/{id}`
- **Web UI:** Decommission button on the asset detail page (`/sandgorgon/assets/{id}`)

All three invoke the same server-side workflow.

## Workflow Steps

### 1. Validate Asset State

The asset must exist and be in a valid state for decommission. The workflow rejects assets that are already `decommissioning` or `retired`. Assets in `available`, `commissioned`, `provisioned`, or `active` states are accepted.

If the asset has a linked envoy, the envoy's certificate is revoked. The envoy will receive an error on its next check-in and cannot re-enroll with the same UUID.

### 2. Set Lifecycle State to Decommissioning

The asset's `lifecycle.state` trait is set to `decommissioning`. This is written to the FleetIndex immediately, making it visible in the fleet dashboard and risk scoring. A decommission event is recorded in `sandgorgon_events`.

### 3. Connect to BMC via Redfish

Sandgorgon opens a Redfish session to the BMC address stored for the asset (`hardware.bmc_address`). Credentials come from the server configuration (`sandgorgon.bmc_username` / `sandgorgon.bmc_password`) or per-asset overrides if configured.

The connection uses HTTPS with TLS verification. Self-signed BMC certificates are accepted by default (most BMCs ship with self-signed certs), but this is configurable.

### 4. Enumerate All Storage Drives

Sandgorgon queries the Redfish Storage subsystem:

```
GET /redfish/v1/Systems/1/Storage
```

For each storage controller, it enumerates all attached drives:

```
GET /redfish/v1/Systems/1/Storage/{controller}/Drives/{drive}
```

Each drive record includes:
- **Model** and **serial number**
- **Media type** (`SSD`, `HDD`, `NVMe`)
- **Capacity**
- **Encryption status** (whether the drive is a Self-Encrypting Drive)
- **Supported sanitization operations** (from `Operations` in the Redfish Drive schema)

If no drives are found, the workflow fails with an error. See [troubleshooting.md](troubleshooting.md) for common causes.

### 5. Select Sanitization Method Per Drive

Each drive gets a sanitization method based on its media type and capabilities. The method determines the NIST 800-88 classification (Clear, Purge, or Destroy) and the verification approach.

| Drive Type | Method | NIST 800-88 Level | Verification | Notes |
|---|---|---|---|---|
| NVMe with CryptoErase or SecureErase | Redfish SecureErase action | **Purge** | NVMe Sanitize Log | Provable completion. Log queried post-erase via Redfish OEM extensions or NVMe-MI. |
| Self-Encrypting Drive (SED) | Crypto-erase via TCG Opal | **Purge** | TCG Opal status | MEK rotation destroys all data. Provable via Opal locking state. |
| SATA/SAS with SecureErase | Redfish SecureErase action | **Clear** | Unverifiable | ATA Secure Erase has no completion proof in SMART data. Certificate explicitly states limitations. |
| No firmware erase support | Boot into wipe image (DBAN/nwipe) | **Clear** | nwipe exit code | Future phase. Not yet implemented. |

The `--method` flag on the CLI allows overriding to a lower method (e.g., `--method clear` forces Clear even on NVMe drives). You cannot override to a higher method than the drive supports.

### 6. Dispatch Sanitization via Redfish SecureErase

For each drive, Sandgorgon issues the Redfish SecureErase action:

```
POST /redfish/v1/Systems/1/Storage/{controller}/Drives/{drive}/Actions/Drive.SecureErase
```

The workflow polls the Redfish task service for completion. Timeouts are per-drive and scale with drive capacity (minimum 30 minutes, maximum 24 hours for large HDDs).

If a drive fails sanitization, the workflow continues with remaining drives but marks the failed drive in the certificate. The overall decommission is flagged as partial.

### 7. Generate NIST 800-88 Certificate

Once all drives have been processed (successfully or not), a NIST 800-88 certificate is generated. The certificate contains per-drive sanitization records with honest verification status. See [nist-800-88.md](nist-800-88.md) for full certificate details.

The certificate is stored in the `sandgorgon_certificates` table and linked to the asset.

### 8. Set Lifecycle State to Retired

The asset's `lifecycle.state` is set to `retired`. If any drives failed sanitization, the state is set to `retired_partial` instead, indicating the asset needs manual attention.

### 9. Write Lifecycle Traits to FleetIndex

The following traits are written for the asset:

- `lifecycle.state` -- `retired` or `retired_partial`
- `sanitization.method` -- the highest method used (Clear or Purge)
- `sanitization.verified` -- `true` only if all drives were verifiably sanitized
- `sanitization.verification_method` -- summary of verification methods used
- `sanitization.certificate_id` -- UUID of the generated certificate
- `sanitization.date` -- ISO 8601 timestamp of completion

These traits are available to risk scoring, compliance reporting, and fleet dashboards.

## Partial Failures

If some drives succeed and others fail:
- A certificate is still generated, covering all drives
- Failed drives are listed with their failure reason
- `lifecycle.state` is set to `retired_partial`
- The asset appears with a warning in the web UI
- The operator can re-attempt decommission for failed drives

## CLI Example

```
$ vigocli sandgorgon decommission web-04.prod.example.com

Decommissioning web-04.prod.example.com...

  Connecting to BMC at 10.0.100.54... OK
  Enumerating drives...
    /dev/nvme0n1  Samsung 980 PRO 1TB   NVMe   Purge (CryptoErase)
    /dev/sda      Seagate ST2000NM 2TB  SATA   Clear (SecureErase)

  Sanitizing /dev/nvme0n1... done (4s, verified via Sanitize Log)
  Sanitizing /dev/sda...     done (3h12m, unverifiable)

  Certificate generated: cert-a1b2c3d4
  Lifecycle state: retired

Certificate exported to: web-04.prod.example.com-nist-800-88.json
```

---
*Confidential -- Alexander4, LLC. Not for redistribution.*
