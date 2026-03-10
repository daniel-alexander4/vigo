package nails

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/hobnob/hobnob/agent/internal/cmdrun"
)

// GPUCollector gathers GPU nails. Detects NVIDIA GPUs via nvidia-smi,
// and falls back to /sys/class/drm for any GPU presence.
type GPUCollector struct{}

func (c *GPUCollector) Name() string { return "gpu" }

func (c *GPUCollector) Collect(ctx context.Context) (map[string]any, error) {
	result := map[string]any{
		"count": 0,
	}

	// Try nvidia-smi for NVIDIA GPUs.
	runner := &cmdrun.Default{}
	out, err := runner.Run(ctx, "nvidia-smi", "--query-gpu=name,driver_version,memory.total,temperature.gpu,utilization.gpu", "--format=csv,noheader,nounits")
	if err == nil {
		var gpus []any
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fields := strings.SplitN(line, ", ", 5)
			gpu := map[string]any{
				"vendor": "nvidia",
			}
			if len(fields) >= 1 {
				gpu["name"] = strings.TrimSpace(fields[0])
			}
			if len(fields) >= 2 {
				gpu["driver_version"] = strings.TrimSpace(fields[1])
			}
			if len(fields) >= 3 {
				gpu["memory_mb"] = strings.TrimSpace(fields[2])
			}
			if len(fields) >= 4 {
				gpu["temperature_c"] = strings.TrimSpace(fields[3])
			}
			if len(fields) >= 5 {
				gpu["utilization_pct"] = strings.TrimSpace(fields[4])
			}
			gpus = append(gpus, gpu)
		}
		if len(gpus) > 0 {
			result["count"] = len(gpus)
			result["devices"] = gpus
			return result, nil
		}
	}

	// Fallback: scan /sys/class/drm for card devices.
	gpus := scanDRMCards()
	if len(gpus) > 0 {
		result["count"] = len(gpus)
		result["devices"] = gpus
	}

	return result, nil
}

func scanDRMCards() []any {
	entries, err := os.ReadDir("/sys/class/drm")
	if err != nil {
		return nil
	}

	var cards []any
	seen := make(map[string]bool)
	for _, e := range entries {
		name := e.Name()
		// Match card0, card1, etc. — skip card0-HDMI-A-1 type entries.
		if !strings.HasPrefix(name, "card") || strings.Contains(name, "-") {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true

		gpu := map[string]any{
			"device": name,
		}

		// Try to read vendor name from the device uevent.
		ueventPath := filepath.Join("/sys/class/drm", name, "device", "uevent")
		if data, err := os.ReadFile(ueventPath); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "DRIVER=") {
					gpu["driver"] = strings.TrimPrefix(line, "DRIVER=")
				}
			}
		}

		cards = append(cards, gpu)
	}

	return cards
}
