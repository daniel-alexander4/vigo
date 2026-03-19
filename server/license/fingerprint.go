// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package license

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
)

// MachineFingerprint computes a stable identifier for the current machine
// by hashing: machine-id + primary MAC address + TLS CA certificate fingerprint.
//
// This binds a license to a specific server installation. The fingerprint
// changes when the OS is reinstalled (new machine-id), the primary NIC is
// replaced (new MAC), or the CA cert is regenerated (new TLS identity).
//
// caFile is the path to the server's CA certificate PEM file. If empty or
// unreadable, only machine-id and MAC are used.
func MachineFingerprint(caFile string) (string, error) {
	var parts []string

	// 1. Machine ID — stable across reboots, changes on OS reinstall or clone.
	mid, err := readMachineID()
	if err != nil {
		return "", fmt.Errorf("reading machine-id: %w", err)
	}
	parts = append(parts, mid)

	// 2. Primary MAC address — stable across reboots, changes on NIC replacement.
	mac, err := primaryMAC()
	if err != nil {
		// MAC is best-effort — some containers don't expose it.
		parts = append(parts, "no-mac")
	} else {
		parts = append(parts, mac)
	}

	// 3. CA certificate fingerprint — ties to this Vigo installation's TLS identity.
	if caFile != "" {
		if fp, err := certFingerprint(caFile); err == nil {
			parts = append(parts, fp)
		}
	}

	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:]), nil
}

// readMachineID reads the OS machine identifier.
func readMachineID() (string, error) {
	switch runtime.GOOS {
	case "linux":
		// /etc/machine-id is present on systemd systems (Debian, RHEL, Fedora, Arch, etc.)
		data, err := os.ReadFile("/etc/machine-id")
		if err != nil {
			// Fallback: /var/lib/dbus/machine-id on older systems.
			data, err = os.ReadFile("/var/lib/dbus/machine-id")
			if err != nil {
				return "", err
			}
		}
		return strings.TrimSpace(string(data)), nil

	case "darwin":
		// macOS: IOPlatformUUID via ioreg is the standard machine identifier.
		// Fall back to /etc/machine-id if it exists (rare on macOS).
		data, err := os.ReadFile("/etc/machine-id")
		if err == nil {
			return strings.TrimSpace(string(data)), nil
		}
		return "", fmt.Errorf("no machine-id on macOS (use ioreg -rd1 -c IOPlatformExpertDevice)")

	case "freebsd", "openbsd", "netbsd":
		// BSD: /etc/hostid or /var/db/host.random
		for _, path := range []string{"/etc/hostid", "/var/db/host.random"} {
			data, err := os.ReadFile(path)
			if err == nil {
				return strings.TrimSpace(string(data)), nil
			}
		}
		return "", fmt.Errorf("no machine identifier found")

	case "windows":
		// Windows: HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid
		// Read via registry — but since this runs on the server (Linux/Docker),
		// Windows is unlikely. Provide a stub.
		return "", fmt.Errorf("windows machine-id not implemented (server runs on Linux)")

	default:
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// primaryMAC returns the MAC address of the first non-loopback, non-virtual interface.
func primaryMAC() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		// Skip loopback, down, and virtual interfaces.
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if len(iface.HardwareAddr) == 0 {
			continue
		}
		// Skip common virtual interface prefixes.
		name := strings.ToLower(iface.Name)
		if strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") ||
			strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "virbr") {
			continue
		}
		return iface.HardwareAddr.String(), nil
	}
	return "", fmt.Errorf("no suitable network interface found")
}

// certFingerprint returns the SHA256 hex digest of a PEM certificate file.
func certFingerprint(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", fmt.Errorf("no PEM block in %s", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(h[:]), nil
}
