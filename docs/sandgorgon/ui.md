# Web UI

Sandgorgon adds pages under the `/sandgorgon/` path in the Vigo web UI. All pages follow the HTMX-first pattern used by the rest of the UI -- server-rendered HTML with HTMX fragments for interactive elements.

## Dashboard

**Path:** `/sandgorgon/`

The dashboard provides an at-a-glance view of the bare metal fleet.

### Stat Cards

Five stat cards across the top of the page:

| Card | Value | Description |
|---|---|---|
| Total Assets | Count | Total number of assets tracked by Sandgorgon |
| Available | Count | Assets in `available` state, ready for commissioning |
| Commissioned | Count | Assets in `commissioned`, `provisioned`, or `active` states |
| Retired | Count | Assets in `retired` or `retired_partial` states |
| Certificates | Count | Total NIST 800-88 certificates generated |

Stat cards update via HTMX polling (same interval as the main Vigo dashboard).

### Asset Table

Below the stat cards, a table lists all tracked assets with sortable columns:

| Column | Description |
|---|---|
| Hostname | Asset hostname, linked to asset detail page |
| Serial Number | Hardware serial from BMC |
| BMC Address | BMC/iDRAC/iLO endpoint |
| Lifecycle State | Current state with color-coded badge |
| Rack Location | Site/rack/unit if assigned |
| Last Event | Most recent lifecycle event (e.g., "Commissioned 2d ago", "Retired 1h ago") |

The table supports:
- **Search:** Filter by hostname, serial number, or asset tag
- **State filter:** Show only assets in a specific lifecycle state
- **Pagination:** Server-side pagination for large fleets

Lifecycle state badges use color coding:
- `available` -- neutral
- `commissioned` / `provisioned` / `active` -- green
- `decommissioning` -- yellow
- `retired` -- gray
- `retired_partial` -- orange (indicates incomplete sanitization)

## Asset Detail

**Path:** `/sandgorgon/assets/{id}`

The detail page for a single asset. Divided into sections:

### Hardware Information

A summary card with hardware details collected from the BMC:

- Manufacturer and model
- Serial number and asset tag
- BMC address and firmware version
- Rack location
- Current lifecycle state

### Linked Envoy

If the asset is linked to a Vigo envoy, this section shows:
- Envoy hostname and UUID
- Current convergence status
- Compliance status
- Link to the envoy's detail page in the main Vigo UI

If the asset is not linked to an envoy (e.g., it is in `available` state), this section displays "No linked envoy."

### Certificates

A table of NIST 800-88 certificates associated with this asset. Most assets will have zero or one certificate. Assets that were returned to the pool and decommissioned again may have multiple.

| Column | Description |
|---|---|
| Certificate ID | Linked to the certificate viewer |
| Issued | Date and time |
| Drives | Number of drives sanitized |
| Method | Highest sanitization method used (Clear or Purge) |
| Result | Overall result: `pass`, `partial`, or `failed` |
| Technician | Username of the operator |

### Lifecycle Events

A chronological timeline of all lifecycle events for this asset:

- Commissioned (date, by whom)
- Provisioned (date, OS image, by whom)
- Enrolled (date, envoy UUID)
- Decommission started (date, by whom)
- Sanitization completed (date, result)
- Retired (date)

Events are stored in `sandgorgon_events` and displayed newest-first.

### Actions

Depending on the asset's current state, action buttons are available:

- **Decommission** -- visible when the asset is in `commissioned`, `provisioned`, or `active` state. Triggers the decommission workflow with a confirmation dialog.
- **Return to Available** -- visible when the asset is in `retired` state (only if all drives were fully sanitized). Resets the asset to `available` for re-use.

## Certificate Archive

**Path:** `/sandgorgon/certificates`

A searchable, sortable table of all NIST 800-88 certificates.

| Column | Description |
|---|---|
| Certificate ID | Linked to the certificate viewer |
| Asset | Hostname of the decommissioned asset, linked to asset detail |
| Issued | Date and time with timezone |
| Drives | Number of drives covered |
| Method | Highest sanitization method (Clear / Purge) |
| Verification | Overall verification result |
| Technician | Username of the operator who initiated decommission |

The table supports:
- **Search:** Filter by certificate ID, hostname, or serial number
- **Date range:** Filter certificates by issue date
- **Export:** Download filtered results as CSV

## Certificate Viewer

**Path:** `/api/v1/sandgorgon/certificates/{id}.html`

A standalone, print-friendly HTML page displaying a single NIST 800-88 certificate. This page is designed to be printed directly from the browser or saved as a PDF.

The certificate viewer is separate from the main UI layout -- it has no navigation bar, sidebar, or other UI chrome. It contains:

- Organization name and logo (if configured)
- Certificate title: "NIST SP 800-88 Rev. 2 Sanitization Certificate"
- Certificate ID
- Issue date and time with timezone
- Asset information (hostname, serial number, asset tag, make/model)
- Technician name
- Per-drive sanitization table:
  - Drive serial and model
  - Media type
  - Capacity
  - Sanitization method and NIST 800-88 level
  - Verification result and method
  - Limitations (if any)
  - Start and end timestamps
- Overall result
- Signature line for technician
- Footer with Vigo version and generation timestamp

The certificate viewer is linked from the certificate archive table, the asset detail page, and the CLI `vigocli sandgorgon certificate` output.

---
*Confidential -- Alexander4, LLC. Not for redistribution.*
