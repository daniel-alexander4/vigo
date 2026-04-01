
# Sandgorgon -- Bare Metal Lifecycle

Sandgorgon is a Vigo subsystem for bare metal lifecycle management. It lives inside vigosrv -- same binary, same SQLite database, same auth system. No separate deployment. CLI via `vigocli sandgorgon`, web UI at `/sandgorgon/`.

Sandgorgon owns the phases that the core Vigo engine does not: what happens before a machine is configured (provisioning) and after it is retired (decommission with auditable proof).

## Lifecycle

```
                         Sandgorgon                    Vigo Core
                    +-----------------+          +------------------+
                    |                 |          |                  |
  [available] ---> [commissioning] ---> [active] ---> [decommissioning] ---> [retired]
                    |                 |          |                  |
                    | BMC registered  |          | Config mgmt      |
                    | Hardware checks |          | Compliance       |
                    | OS provisioned  |          | Risk scoring     |
                    | Agent enrolled  |          | Trait collection  |
                    |                 |          |                  |
                    +-----------------+          +------------------+
                                                        |
                                                        v
                                                NIST 800-88 certificate
                                                Disk sanitization
                                                Cert revocation
```

## Phases

**Phase 1 (current): Decommission + NIST 800-88 Certificates.** Commission existing machines by registering their BMC address, trigger decommission workflows (cert revocation, disk sanitization, verification), and generate auditable NIST 800-88 certificates. CSV import for bulk onboarding.

**Phase 2: Asset Tracking.** Full hardware inventory with lifecycle state machine, import from NetBox and Snipe-IT, rack location tracking, fleet dashboard lifecycle distribution.

**Phase 3: Provisioning.** PXE/iPXE boot chain, OS image management, preseed/kickstart/autoinstall templates, DHCP integration, automatic Vigo agent enrollment as the final provisioning step.

## Documentation

- [Getting Started](getting-started.md) -- prerequisites and quickstart walkthrough
- [Configuration](configuration.md) -- server.yaml sandgorgon section
- [CLI Reference](cli-reference.md) -- all `vigocli sandgorgon` subcommands
- [API Reference](api-reference.md) -- REST endpoints
- [Import](import.md) -- CSV format spec and bulk import

---
*Confidential -- Alexander4, LLC. Not for redistribution.*
