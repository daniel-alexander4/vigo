# Provisioning

Sandgorgon provisioning manages the Day 0 lifecycle of bare metal servers: from power-on through OS installation to automatic Vigo agent enrollment.

## Overview

1. Place OS images in `/srv/vigo/sandgorgon/images/`
2. Register images: `vigogo sandgorgon images-register`
3. Commission the asset: `vigogo sandgorgon commission`
4. Provision: `vigogo sandgorgon provision <hostname> --image <name>`
5. Machine PXE boots, installs OS, enrolls in Vigo automatically

## OS Image Management

Sandgorgon does not ship OS images. Place your organization's approved images in `/srv/vigo/sandgorgon/images/` and register them.

Supported formats:
- **ISO** (.iso) -- served via HTTP for iPXE `sanboot`
- **Kernel + initrd** -- direct network boot via iPXE `kernel`/`initrd`

Register an image:

```
vigogo sandgorgon images-register \
  --name ubuntu-24.04-amd64 \
  --path /srv/vigo/sandgorgon/images/ubuntu-24.04-live-server-amd64.iso \
  --os-family ubuntu \
  --os-version 24.04
```

List registered images:

```
vigogo sandgorgon images
```

Remove a registration (does not delete the file):

```
vigogo sandgorgon images-remove ubuntu-24.04-amd64
```

## PXE/iPXE Boot Chain

Two-stage boot process:

1. **Stage 1:** Machine firmware loads iPXE binary from your TFTP server (BIOS: `undionly.kpxe`, UEFI: `ipxe.efi`)
2. **Stage 2:** iPXE fetches a machine-specific boot script from `GET /sandgorgon/boot/{asset-id}`, which selects the image and chains into the OS installer

The iPXE script is dynamically generated per asset based on the provisioning request.

## Configuration Templates

Each OS family uses its own unattended installation format. Sandgorgon generates these automatically:

| OS Family | Format | Served at |
|-----------|--------|-----------|
| Debian | preseed | `GET /sandgorgon/preseed/{image-id}?hostname=...` |
| Ubuntu Server | autoinstall (cloud-init) | Same endpoint |
| RHEL/Rocky/Alma | kickstart | Same endpoint |

All templates include Vigo agent installation and enrollment as the final step.

## DHCP Integration

Sandgorgon does **not** include a built-in DHCP server. Configure your existing DHCP to point PXE clients at the Sandgorgon endpoints.

Generate config snippets:

```
vigogo sandgorgon dhcp-config --type isc       # ISC DHCP
vigogo sandgorgon dhcp-config --type kea        # Kea DHCP4
vigogo sandgorgon dhcp-config --type dnsmasq    # dnsmasq
```

This is a deliberate design choice. DHCP is infrastructure-critical and should not be owned by a new tool.

## Auto-Enrollment

The final step of every provisioning template:

1. Post-install script downloads the Vigo agent from `GET /bootstrap`
2. Agent installs as a system service
3. Optional enrollment token (via `--token`) enables trusted enrollment
4. Agent starts first check-in cycle

The machine transitions from `provisioned` to `active` once the agent reports.

## Provision Workflow

```
vigogo sandgorgon provision web01.example.com --image ubuntu-24.04-amd64
```

What happens:
1. Validates asset state (must be commissioned or available)
2. Looks up the registered image
3. Sets one-time PXE boot via Redfish (`BootSourceOverrideTarget: Pxe`)
4. Power cycles the machine via Redfish
5. Transitions asset to `provisioned` state
6. Machine PXE boots → iPXE loads → fetches boot script → installs OS → enrolls in Vigo

## CLI Commands

```
vigogo sandgorgon images                    # List registered images
vigogo sandgorgon images-register [flags]   # Register an image
vigogo sandgorgon images-remove <name>      # Remove registration
vigogo sandgorgon provision <host> --image <name>  # Provision a machine
vigogo sandgorgon dhcp-config --type <type> # Generate DHCP config snippet
```

## API Endpoints

```
GET    /api/v1/sandgorgon/images            # List images
POST   /api/v1/sandgorgon/images            # Register image
DELETE /api/v1/sandgorgon/images/{id}        # Remove image
POST   /api/v1/sandgorgon/provision/{id}     # Provision asset
GET    /api/v1/sandgorgon/boot/{id}          # iPXE boot script
GET    /api/v1/sandgorgon/preseed/{id}       # Preseed/autoinstall/kickstart config
GET    /api/v1/sandgorgon/dhcp-config        # DHCP config snippet
```

---
*Confidential -- Alexander4, LLC. Not for redistribution.*
