# Asset Tracking (Phase 2)

**Status: Planned -- not yet implemented.**

This document describes the planned asset tracking capabilities for Sandgorgon Phase 2. Phase 1 (decommission + NIST 800-88 certificates) is the current focus. See [decommission.md](decommission.md) for available features.

Note: Phase 1 already writes lifecycle traits during commission and decommission. Phase 2 expands this into a full asset tracking system with a state machine, hardware inventory, and external system integration.

## Lifecycle State Machine

Every Sandgorgon asset has a lifecycle state. The state machine defines valid transitions:

```
available --> commissioned --> provisioned --> active --> decommissioning --> retired
    ^                                                                          |
    +--- (return to pool, Phase 3) -------------------------------------------+
```

| State | Description |
|---|---|
| `available` | Hardware registered but not assigned to any workload. BMC address known, ready for commissioning or provisioning. |
| `commissioned` | BMC validated, hardware inventory collected, asset assigned to a role or workload but OS not yet installed. |
| `provisioned` | OS installed (via Sandgorgon or externally), Vigo agent not yet enrolled or not yet checked in. |
| `active` | Vigo agent enrolled and reporting. The machine is in operational service. This is the steady state managed by the core Vigo engine. |
| `decommissioning` | Decommission workflow in progress. Envoy certificate revoked, sanitization underway. |
| `retired` | Decommission complete. Sanitization certificate generated. Machine is out of service. |

Invalid transitions are rejected. For example, an `active` machine cannot go directly to `retired` -- it must pass through `decommissioning`.

## Hardware Inventory

Phase 2 will collect and store hardware details for each asset, sourced from the BMC via Redfish during commissioning:

- Manufacturer and model
- Serial number
- BMC firmware version
- CPU count, model, and core count
- Total RAM
- Storage drives (model, serial, capacity, media type)
- Network interfaces (MAC addresses, link speed)

This data will be stored in the `sandgorgon_assets` table and exposed as traits.

## Rack Location

Each asset can have a rack location assigned:

- **Site** -- data center or facility name
- **Rack** -- rack identifier
- **Unit** -- rack unit position (U number)

Rack location is set during commissioning or import and updated via CLI or API. It is informational and does not affect any automated workflows.

## External System Integration

### Import from CSV

For organizations with existing asset databases, Sandgorgon will accept CSV imports:

```
vigogo sandgorgon import --source csv --file assets.csv
```

CSV columns: `hostname`, `bmc_address`, `serial_number`, `asset_tag`, `rack_location`

- The `bmc_address` column is required
- Rows matching an enrolled envoy hostname are linked to that envoy
- Unmatched rows create asset records in `available` state

### Import from NetBox

```
vigogo sandgorgon import --source netbox --url https://netbox.example.com --token <token>
```

NetBox devices will be mapped to Sandgorgon assets using serial number and hostname matching.

### Import from Snipe-IT

```
vigogo sandgorgon import --source snipe-it --url https://snipeit.example.com --token <token>
```

Snipe-IT assets will be mapped using serial number and asset tag matching.

All imports are idempotent. Re-running an import updates existing records rather than creating duplicates. Matching is by serial number first, then hostname, then asset tag.

## FleetIndex Traits

Sandgorgon writes the following traits to the Vigo FleetIndex. These traits are available to risk scoring, compliance reporting, conditional expressions, and fleet dashboards.

### Lifecycle Traits

| Trait Key | Type | Description |
|---|---|---|
| `lifecycle.state` | string | Current lifecycle state (`available`, `commissioned`, `provisioned`, `active`, `decommissioning`, `retired`) |
| `lifecycle.provisioned_at` | string | ISO 8601 timestamp of when the machine was provisioned |

### Hardware Traits

| Trait Key | Type | Description |
|---|---|---|
| `hardware.bmc_address` | string | BMC/iDRAC/iLO endpoint address (IP or hostname) |
| `hardware.bmc_firmware` | string | BMC firmware version string |
| `hardware.serial_number` | string | Hardware serial number from BMC |
| `hardware.asset_tag` | string | Organization-assigned asset tag |
| `hardware.rack_location` | string | Site/rack/unit location (e.g., `dc1/rack-14/U22`) |

### Trait Sources

These traits are written directly by the Sandgorgon subsystem within vigosrv. Because Sandgorgon is part of the same server process, it writes to the FleetIndex and database through an internal path -- no file-based collector or agent-side trait reporting is needed.

This means traits are available even when the agent is not running (during commissioning before the agent is installed, or during decommission after the agent's certificate has been revoked).

### What Traits Unlock

- **Risk scoring:** Lifecycle state as a risk factor. A machine pending decommission (`decommissioning`) has elevated risk. A machine in `available` state with no agent has unknown risk.
- **Compliance reporting:** PCI DSS 9.4 and HIPAA 164.310(d)(2) controls can reference lifecycle and sanitization traits as evidence.
- **Fleet dashboards:** Lifecycle state distribution charts (how many machines are active, commissioned, retired).
- **Conditional expressions:** Module `when:` expressions can reference lifecycle traits. For example, `when: "lifecycle.state == 'active'"` to skip modules on machines being decommissioned.
- **Audit trail:** Full chain from provisioning through operational life to decommission, all in one system.

---
*Confidential -- Alexander4, LLC. Not for redistribution.*
