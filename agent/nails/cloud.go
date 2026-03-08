package nails

import (
	"context"
	"os"
	"runtime"
	"strings"
)

// CloudCollector detects cloud provider from DMI strings on Linux.
// Returns provider name ("aws", "gcp", "azure", "digitalocean", "oracle",
// "hetzner") or "none" if no cloud provider is detected.
type CloudCollector struct{}

func (c *CloudCollector) Name() string { return "cloud" }

func (c *CloudCollector) Collect(_ context.Context) (map[string]any, error) {
	if runtime.GOOS != "linux" {
		return map[string]any{"provider": "unknown"}, nil
	}

	provider := detectProvider()

	return map[string]any{
		"provider": provider,
	}, nil
}

func detectProvider() string {
	// Read DMI fields that indicate cloud providers.
	sysVendor := readDMI("/sys/class/dmi/id/sys_vendor")
	productName := readDMI("/sys/class/dmi/id/product_name")
	boardVendor := readDMI("/sys/class/dmi/id/board_vendor")
	biosVendor := readDMI("/sys/class/dmi/id/bios_vendor")

	all := strings.ToLower(sysVendor + " " + productName + " " + boardVendor + " " + biosVendor)

	switch {
	case strings.Contains(all, "amazon") || strings.Contains(all, "ec2"):
		return "aws"
	case strings.Contains(all, "google"):
		return "gcp"
	case strings.Contains(all, "microsoft corporation"):
		return "azure"
	case strings.Contains(all, "digitalocean"):
		return "digitalocean"
	case strings.Contains(all, "oracle"):
		return "oracle"
	case strings.Contains(all, "hetzner"):
		return "hetzner"
	case strings.Contains(all, "vultr"):
		return "vultr"
	case strings.Contains(all, "linode") || strings.Contains(all, "akamai"):
		return "linode"
	}

	return "none"
}

func readDMI(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
