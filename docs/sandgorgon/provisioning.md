# Provisioning (Phase 3)

**Status: Planned -- not yet implemented.**

This document describes the planned provisioning capabilities for Sandgorgon Phase 3. None of the features described here are available in the current release. This page exists to document the design intent and scope.

Phase 1 (decommission + NIST 800-88 certificates) and Phase 2 (asset tracking) will be completed first. See [decommission.md](decommission.md) and [asset-tracking.md](asset-tracking.md) for currently available and in-progress features.

## Overview

Sandgorgon provisioning will manage the Day 0 lifecycle of bare metal servers: from power-on through OS installation to automatic Vigo agent enrollment. The goal is a single workflow that takes a machine from "available" to "active" with no manual intervention after initial configuration.

## PXE/iPXE Boot Chain

Provisioning will use a two-stage PXE boot process:

1. **Stage 1 (PXE/UEFI HTTP Boot):** The machine's firmware loads an iPXE binary from the Sandgorgon TFTP or HTTP endpoint. Both BIOS (legacy PXE) and UEFI (HTTP Boot) firmware will be supported.

2. **Stage 2 (iPXE Script):** The iPXE binary fetches a machine-specific iPXE script from the Sandgorgon HTTP endpoint. This script identifies the machine (by MAC address, serial number, or BMC-provided UUID), selects the correct OS image and configuration template, and chains into the OS installer.

The iPXE script will be dynamically generated per machine based on the provisioning request.

## OS Image Management

Sandgorgon will maintain a library of OS installation images:

- **Debian/Ubuntu** -- netboot images with preseed configuration
- **RHEL/Rocky/AlmaLinux** -- netboot images with kickstart configuration
- **Ubuntu Server (20.04+)** -- autoinstall (cloud-init based) configuration
- **Windows Server** -- WinPE with autounattend.xml (future consideration)

Images will be stored on the Sandgorgon server and served via HTTP. Image management will include version tracking and checksum verification.

## Configuration Templates

Each OS family uses its own unattended installation format:

- **Preseed** (Debian/Ubuntu): `preseed.cfg` templates with Go template syntax for variable substitution (hostname, network config, partitioning, packages)
- **Kickstart** (RHEL/Rocky): `ks.cfg` templates with the same variable substitution
- **Autoinstall** (Ubuntu Server): `user-data` YAML templates following the cloud-init autoinstall schema

Templates will support:
- Disk partitioning layouts (standard, LVM, ZFS)
- Network configuration (static IP, bonding, VLAN tagging)
- Package selection
- Post-install scripts
- Vigo agent installation and enrollment as the final step

## DHCP Integration

Sandgorgon will NOT include a built-in DHCP server. PXE boot requires DHCP, and organizations already have DHCP infrastructure. Sandgorgon will integrate with existing DHCP servers:

- **ISC DHCP / Kea:** Documentation for configuring `next-server` and `filename` options to point at the Sandgorgon TFTP/HTTP endpoint.
- **dnsmasq:** Configuration examples for `dhcp-boot` and `tftp-root` options.
- **Vendor DHCP (Infoblox, BlueCat):** Generic guidance for setting PXE boot options (Option 66/67 for BIOS, Option 59/60 for UEFI HTTP Boot).

This is a deliberate design choice. DHCP is infrastructure-critical and organizations should not have to trust a new tool with it.

## Auto-Enrollment in Vigo

The final step of every provisioning workflow will be automatic Vigo agent installation and enrollment:

1. The post-install script downloads the Vigo agent binary from the Sandgorgon HTTP endpoint
2. The agent is installed as a system service
3. A one-time enrollment token (pre-generated during the provisioning request) is used to register the agent with the Vigo server
4. The agent begins its first check-in cycle

The machine transitions from `provisioned` to `active` once the agent successfully completes its first check-in.

## Planned CLI Commands

```
vigogo sandgorgon provision <hostname> --image <image> --role <role>
vigogo sandgorgon images list
vigogo sandgorgon images add --os <os> --version <version> --arch <arch> --url <url>
```

## Planned API Endpoints

```
POST /api/v1/sandgorgon/provision
GET  /api/v1/sandgorgon/images
POST /api/v1/sandgorgon/images
```

## Risks and Considerations

- **PXE is fragile.** BIOS vs. UEFI boot, network topology, DHCP relay agents, and vendor-specific firmware quirks make PXE provisioning one of the most difficult things to make reliable across heterogeneous hardware.
- **Per-distro maintenance burden.** Preseed, kickstart, and autoinstall formats change between OS releases. Templates will need ongoing updates.
- **Competitive landscape.** Foreman, MAAS, and Tinkerbell are mature provisioning tools. Sandgorgon's differentiation is the unified lifecycle (provision through decommission in one tool), not provisioning features alone.
- **Scope.** This is the largest Sandgorgon phase. It will be implemented after decommission and asset tracking are proven in production.

---
*Confidential -- Alexander4, LLC. Not for redistribution.*
