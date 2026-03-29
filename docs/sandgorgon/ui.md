
# Web UI

Sandgorgon adds three pages to the Vigo web UI, accessible from the "Bare Metal" section in the sidebar.

## Dashboard (`/sandgorgon`)

The main Sandgorgon page shows:

- **Stat cards** -- Total assets, available, commissioned, retired, and certificate count
- **Assets table** -- All managed hardware with hostname, BMC address, serial number, lifecycle state, and rack location. Click any row to see the asset detail page.

## Asset Detail (`/sandgorgon/assets/{id}`)

Detailed view of a single hardware asset:

- **Hardware details** -- BMC address, asset tag, rack location, linked envoy (if any), commission/decommission timestamps
- **NIST 800-88 certificates** -- All certificates generated for this asset, with method, verification result, technician, and a link to the printable HTML certificate
- **Lifecycle events** -- Chronological event log (commissioned, decommission_started, drive_sanitized, decommission_completed, etc.)

## Certificates (`/sandgorgon/certificates`)

Archive of all NIST 800-88 media sanitization certificates across all assets:

- Hostname, serial number, sanitization method, media type, verification result, technician, issue date
- **View** button opens the print-friendly HTML certificate in a new tab

## Certificate Viewer (`/api/v1/sandgorgon/certificates/{id}.html`)

Self-contained, print-friendly HTML page suitable for auditors:

- Asset identification (hostname, serial, model, asset tag, media type)
- Sanitization details (method, start/end time, technician)
- Verification (method, result, limitations if applicable)
- Certificate ID and issue date

Print directly from the browser or save as PDF for compliance records.

---
*Confidential -- Alexander4, LLC. Not for redistribution.*
