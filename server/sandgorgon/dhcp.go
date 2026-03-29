// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package sandgorgon

import "fmt"

// DHCPConfig generates DHCP configuration snippets for various DHCP servers.
// These point PXE clients to the Vigo iPXE boot endpoint.

// GenerateDHCPConfig returns a config snippet for the specified DHCP server type.
func GenerateDHCPConfig(serverAddr, dhcpType string) (string, error) {
	switch dhcpType {
	case "isc", "isc-dhcp":
		return generateISCDHCP(serverAddr), nil
	case "kea":
		return generateKea(serverAddr), nil
	case "dnsmasq":
		return generateDnsmasq(serverAddr), nil
	default:
		return "", fmt.Errorf("unknown DHCP server type: %s (supported: isc, kea, dnsmasq)", dhcpType)
	}
}

func generateISCDHCP(serverAddr string) string {
	return fmt.Sprintf(`# ISC DHCP Server — Sandgorgon PXE boot configuration
# Add this to /etc/dhcp/dhcpd.conf

# iPXE chainloading: serve undionly.kpxe to legacy BIOS, ipxe.efi to UEFI.
# Once iPXE is loaded, redirect to the Sandgorgon boot script endpoint.

option architecture-type code 93 = unsigned integer 16;

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

    next-server <TFTP-SERVER-IP>;
}

# After iPXE loads, it will chain to this URL for the boot script:
# %s/sandgorgon/boot/{asset-id}
# Configure iPXE to embed this URL or use DHCP option 175.
`, serverAddr)
}

func generateKea(serverAddr string) string {
	return fmt.Sprintf(`// Kea DHCP4 — Sandgorgon PXE boot configuration
// Add to the "Dhcp4" section in kea-dhcp4.conf

{
    "client-classes": [
        {
            "name": "ipxe-uefi",
            "test": "substring(option[93].hex, 0, 4) == '0007' or substring(option[93].hex, 0, 4) == '0009'",
            "boot-file-name": "ipxe.efi",
            "next-server": "<TFTP-SERVER-IP>"
        },
        {
            "name": "ipxe-bios",
            "test": "substring(option[60].text, 0, 9) == 'PXEClient'",
            "boot-file-name": "undionly.kpxe",
            "next-server": "<TFTP-SERVER-IP>"
        }
    ]
}

// After iPXE loads, it chains to:
// %s/sandgorgon/boot/{asset-id}
`, serverAddr)
}

func generateDnsmasq(serverAddr string) string {
	return fmt.Sprintf(`# dnsmasq — Sandgorgon PXE boot configuration
# Add to /etc/dnsmasq.conf or a file in /etc/dnsmasq.d/

# Enable TFTP
enable-tftp
tftp-root=/var/lib/tftpboot

# Tag UEFI and BIOS clients differently
dhcp-match=set:efi-x86_64,option:client-arch,7
dhcp-match=set:efi-x86_64,option:client-arch,9
dhcp-match=set:bios,option:client-arch,0

# Serve the appropriate iPXE binary
dhcp-boot=tag:efi-x86_64,ipxe.efi
dhcp-boot=tag:bios,undionly.kpxe

# After iPXE loads, it chains to:
# %s/sandgorgon/boot/{asset-id}
`, serverAddr)
}
