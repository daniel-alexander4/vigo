// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package traits

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// BlockDevicesCollector gathers block device traits from /sys/block/ on Linux.
type BlockDevicesCollector struct{}

func (c *BlockDevicesCollector) Name() string { return "blockdevices" }

func (c *BlockDevicesCollector) Collect(_ context.Context) (map[string]any, error) {
	if runtime.GOOS != "linux" {
		return map[string]any{}, nil
	}

	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}

	devices := make(map[string]any)
	for _, entry := range entries {
		name := entry.Name()
		// Skip loop and ram devices.
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") {
			continue
		}

		base := filepath.Join("/sys/block", name)

		// Read size (in 512-byte sectors).
		sizeData, err := os.ReadFile(filepath.Join(base, "size"))
		if err != nil {
			continue
		}
		sectors, err := strconv.ParseUint(strings.TrimSpace(string(sizeData)), 10, 64)
		if err != nil {
			continue
		}
		sizeBytes := sectors * 512

		// Read model (may not exist for virtual devices).
		model := ""
		if data, err := os.ReadFile(filepath.Join(base, "device", "model")); err == nil {
			model = strings.TrimSpace(string(data))
		}

		// Read readonly flag.
		readonly := false
		if data, err := os.ReadFile(filepath.Join(base, "ro")); err == nil {
			readonly = strings.TrimSpace(string(data)) == "1"
		}

		devices[name] = map[string]any{
			"size_bytes": sizeBytes,
			"model":      model,
			"readonly":   readonly,
		}
	}

	return devices, nil
}
