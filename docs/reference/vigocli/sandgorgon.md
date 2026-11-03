
# vigocli sandgorgon

Bare-metal lifecycle CLI — commission, decommission, provisioning, and NIST 800-88 sanitization certificates. Requires the [Sandgorgon](../../overview.md) subsystem to be enabled on the server.

## Subcommands

| Verb | Purpose |
|---|---|
| `status` | Show all managed hardware assets and their lifecycle state |
| `commission [hostname-or-id]` | Register hardware for bare-metal lifecycle management |
| `decommission <asset-id-or-hostname>` | Decommission hardware: revoke cert, sanitize disks, generate NIST 800-88 certificate |
| `return-to-available <asset-id-or-hostname>` | Return a retired asset to the available pool for re-use |
| `provision <asset-id-or-hostname>` | PXE boot an asset with a registered OS image |
| `import --source {csv\|netbox\|snipeit} --file <path>` | Import hardware assets from external inventory |
| `report <asset-id-or-hostname>` | Generate a full lifecycle report for an asset |
| `events <asset-id-or-hostname>` | Show lifecycle events for an asset |
| `images` | List registered OS images |
| `images-register` | Register an OS image for provisioning |
| `images-remove <name>` | Remove an image registration (does not delete the file) |
| `certificate <certificate-id>` | View or export a NIST 800-88 sanitization certificate |
| `certificates` | List all NIST 800-88 certificates |
| `dhcp-config` | Generate DHCP server config snippet for PXE boot |

Run `vigocli sandgorgon --help` for current flags and defaults.

## See also

- [Sandgorgon](../../overview.md) — bare-metal lifecycle subsystem overview
