# Provisioning

Sandgorgon provisioning manages the Day 0 lifecycle of bare metal servers: from power-on through OS installation to automatic Vigo agent enrollment.

## Overview

1. Place OS images in `/srv/vigo/sandgorgon/images/`
2. Register images: `vigocli sandgorgon images-register`
3. Commission the asset: `vigocli sandgorgon commission`
4. Provision: `vigocli sandgorgon provision <hostname> --image <name>`
5. Machine PXE boots, installs OS, enrolls in Vigo automatically

## OS Image Management

Sandgorgon does not ship OS images. Place your organization's approved images in `/srv/vigo/sandgorgon/images/` and register them.

Supported formats:
- **ISO** (.iso) -- served via HTTP for iPXE `sanboot`
- **Kernel + initrd** -- direct network boot via iPXE `kernel`/`initrd`

Register an image:

```
vigocli sandgorgon images-register \
  --name ubuntu-24.04-amd64 \
  --path /srv/vigo/sandgorgon/images/ubuntu-24.04-live-server-amd64.iso \
  --os-family ubuntu \
  --os-version 24.04
```

List registered images:

```
vigocli sandgorgon images
```

Remove a registration (does not delete the file):

```
vigocli sandgorgon images-remove ubuntu-24.04-amd64
```

## PXE/iPXE Boot Chain

Two-stage boot process:

1. **Stage 1:** Machine firmware loads iPXE binary from your TFTP server (BIOS: `undionly.kpxe`, UEFI: `ipxe.efi`)
2. **Stage 2:** iPXE fetches a machine-specific boot script from `GET /sandgorgon/boot/{asset-id}`, which selects the image and chains into the OS installer

The iPXE script is dynamically generated per asset based on the provisioning request.

You need two iPXE binaries on your TFTP server:
- `undionly.kpxe` — for legacy BIOS PXE clients
- `ipxe.efi` — for UEFI PXE clients

Download these from [ipxe.org](https://ipxe.org) or build them with an embedded script that chains to Sandgorgon:

```
#!ipxe
dhcp
chain https://<vigo-server>:8443/sandgorgon/boot/${uuid}
```

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
vigocli sandgorgon dhcp-config --type isc       # ISC DHCP
vigocli sandgorgon dhcp-config --type kea        # Kea DHCP4
vigocli sandgorgon dhcp-config --type dnsmasq    # dnsmasq
```

This is a deliberate design choice. DHCP is infrastructure-critical and should not be owned by a new tool. If you want Vigo to manage a dedicated DHCP server on the same box, see the `sandgorgon-dhcp` example module below.

### ISC DHCP Server (dhcpd)

Add to `/etc/dhcp/dhcpd.conf`:

```
# Sandgorgon PXE boot configuration
# Chainloads iPXE, which then fetches boot scripts from Sandgorgon.

option architecture-type code 93 = unsigned integer 16;

# Subnet for your provisioning VLAN
subnet 10.0.10.0 netmask 255.255.255.0 {
    range 10.0.10.100 10.0.10.200;
    option routers 10.0.10.1;
    option domain-name-servers 10.0.10.1;

    class "pxeclients" {
        match if substring (option vendor-class-identifier, 0, 9) = "PXEClient";

        # UEFI clients
        if option architecture-type = 00:07 or option architecture-type = 00:09 {
            filename "ipxe.efi";
        }
        # Legacy BIOS clients
        else {
            filename "undionly.kpxe";
        }

        # TFTP server hosting iPXE binaries
        next-server 10.0.10.5;
    }
}
```

Replace `10.0.10.5` with the IP of your TFTP server (often the same box as Vigo). Adjust the subnet, range, and gateway for your network.

### Kea DHCP4

Add to the `"Dhcp4"` section in `/etc/kea/kea-dhcp4.conf`:

```json
{
    "client-classes": [
        {
            "name": "ipxe-uefi",
            "test": "substring(option[93].hex, 0, 4) == '0007' or substring(option[93].hex, 0, 4) == '0009'",
            "boot-file-name": "ipxe.efi",
            "next-server": "10.0.10.5"
        },
        {
            "name": "ipxe-bios",
            "test": "substring(option[60].text, 0, 9) == 'PXEClient'",
            "boot-file-name": "undionly.kpxe",
            "next-server": "10.0.10.5"
        }
    ],
    "subnet4": [
        {
            "subnet": "10.0.10.0/24",
            "pools": [{ "pool": "10.0.10.100 - 10.0.10.200" }],
            "option-data": [
                { "name": "routers", "data": "10.0.10.1" },
                { "name": "domain-name-servers", "data": "10.0.10.1" }
            ]
        }
    ]
}
```

### dnsmasq

Add to `/etc/dnsmasq.conf` or a file in `/etc/dnsmasq.d/`:

```
# Sandgorgon PXE boot configuration

# Enable built-in TFTP server
enable-tftp
tftp-root=/var/lib/tftpboot

# DHCP range for provisioning VLAN
dhcp-range=10.0.10.100,10.0.10.200,255.255.255.0,12h

# Tag UEFI and BIOS clients
dhcp-match=set:efi-x86_64,option:client-arch,7
dhcp-match=set:efi-x86_64,option:client-arch,9
dhcp-match=set:bios,option:client-arch,0

# Serve the appropriate iPXE binary
dhcp-boot=tag:efi-x86_64,ipxe.efi
dhcp-boot=tag:bios,undionly.kpxe

# Standard options
dhcp-option=option:router,10.0.10.1
dhcp-option=option:dns-server,10.0.10.1
```

dnsmasq is the simplest option — it provides DHCP and TFTP in a single process. This is the recommended choice for a dedicated provisioning DHCP server managed by Vigo (see the example module below).

### TFTP Setup

All three DHCP configurations require a TFTP server hosting the iPXE binaries. If you use dnsmasq, its built-in TFTP is already configured above. For ISC or Kea, install a standalone TFTP server:

```bash
# Debian/Ubuntu
apt install tftpd-hpa

# RHEL/Rocky
dnf install tftp-server
```

Place the iPXE binaries:

```bash
mkdir -p /var/lib/tftpboot
curl -o /var/lib/tftpboot/undionly.kpxe https://boot.ipxe.org/undionly.kpxe
curl -o /var/lib/tftpboot/ipxe.efi https://boot.ipxe.org/ipxe.efi
```

### Dedicated DHCP Module

If you want Vigo to manage a dnsmasq-based DHCP/TFTP server on the Vigo server itself (for example, on a dedicated provisioning VLAN), use the `sandgorgon-dhcp` example module:

```bash
cp example-configs/modules/sandgorgon-dhcp.vgo.example /srv/vigo/stack/stage/modules/sandgorgon-dhcp.vgo
```

See `example-configs/modules/sandgorgon-dhcp.vgo.example` for the full module. It installs dnsmasq, deploys the PXE config, downloads iPXE binaries, and manages the service — all idempotent and convergence-tracked.

## Auto-Enrollment

The final step of every provisioning template:

1. Post-install script downloads the Vigo agent from `GET /bootstrap`
2. Agent installs as a system service
3. Optional enrollment token (via `--token`) enables trusted enrollment
4. Agent starts first check-in cycle

The machine transitions from `provisioned` to `active` once the agent reports.

## Provision Workflow

```
vigocli sandgorgon provision web01.example.com --image ubuntu-24.04-amd64
```

What happens:
1. Validates asset state (must be commissioned or available)
2. Looks up the registered image
3. Sets one-time PXE boot via Redfish (`BootSourceOverrideTarget: Pxe`)
4. Power cycles the machine via Redfish
5. Transitions asset to `provisioned` state
6. Machine PXE boots -> iPXE loads -> fetches boot script -> installs OS -> enrolls in Vigo

## CLI Commands

```
vigocli sandgorgon images                    # List registered images
vigocli sandgorgon images-register [flags]   # Register an image
vigocli sandgorgon images-remove <name>      # Remove registration
vigocli sandgorgon provision <host> --image <name>  # Provision a machine
vigocli sandgorgon dhcp-config --type <type> # Generate DHCP config snippet
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
