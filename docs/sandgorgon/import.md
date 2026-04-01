
# Sandgorgon Import

Sandgorgon supports bulk import of hardware assets from CSV files. This is the primary onboarding path for organizations with existing hardware inventories.

## CSV Format

### Columns

| Column | Required | Description |
|--------|----------|-------------|
| `bmc_address` | Yes | BMC IP address or hostname (Redfish/IPMI endpoint) |
| `hostname` | No | Machine hostname. Used for envoy matching |
| `serial_number` | No | Hardware serial number |
| `asset_tag` | No | Organization's asset tag |
| `rack_location` | No | Physical location (e.g., "DC1-R04-U12") |
| `bmc_username` | No | BMC username for this machine |
| `bmc_password` | No | BMC password. Supports `secret:` prefix |
| `model` | No | Hardware model (e.g., "Dell PowerEdge R660") |

Column order does not matter. The header row is required. Extra columns are ignored.

### Hostname Matching

When a row includes a `hostname` value:

- If the hostname matches an enrolled Vigo envoy, the asset is **linked** to that envoy and its state is set to `commissioned`.
- If the hostname does not match any enrolled envoy, the asset is created in `available` state.

Rows without a hostname are always created in `available` state.

### Example CSV

```csv
bmc_address,hostname,serial_number,asset_tag,rack_location,model,bmc_username,bmc_password
10.0.1.101,web01.example.com,SN-2024-00451,A-00451,DC1-R04-U12,Dell PowerEdge R660,admin,secret:sandgorgon/bmc/default
10.0.1.102,db01.example.com,SN-2024-00452,A-00452,DC1-R04-U14,Dell PowerEdge R760,admin,secret:sandgorgon/bmc/default
10.0.1.103,gpu03.example.com,SN-2024-00510,A-00510,DC1-R08-U01,Supermicro SYS-421GE,admin,secret:sandgorgon/bmc/gpu
10.0.1.104,,SN-2024-00600,A-00600,DC1-R10-U20,HP ProLiant DL380,,
10.0.1.105,cache01.example.com,SN-2024-00601,A-00601,DC1-R10-U22,HP ProLiant DL380,admin,secret:sandgorgon/bmc/default
```

In this example, if `web01.example.com`, `db01.example.com`, and `cache01.example.com` are enrolled Vigo envoys, those three assets are linked (state: `commissioned`). The row for `10.0.1.104` has no hostname, so it is created as `available`. The row for `gpu03.example.com` depends on whether that hostname is enrolled.

## CLI Usage

```bash
vigocli sandgorgon import --source csv --file /path/to/assets.csv
```

Example output:

```
Imported 5 assets: 3 linked to envoys, 2 available
```

## API Usage

```bash
curl -X POST https://vigo.example.com:8443/api/v1/sandgorgon/import \
  -H "Authorization: Bearer vgot_..." \
  -F "source=csv" \
  -F "file=@/path/to/assets.csv"
```

See [API Reference](api-reference.md) for the full response format.

## Future Import Sources

Phase 2 will add direct API integration with asset management systems:

- **NetBox** -- import via NetBox REST API with device-to-asset field mapping.
- **Snipe-IT** -- import via Snipe-IT REST API with asset-to-Sandgorgon field mapping.

These will use `--source netbox` and `--source snipe-it` flags with `--url` and `--token` parameters.

---
*Confidential -- Alexander4, LLC. Not for redistribution.*
