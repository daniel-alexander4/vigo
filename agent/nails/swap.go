package nails

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
)

// SwapCollector gathers swap device nails from /proc/swaps.
// Returns a list of active swap entries with path, type, size, used, priority.
type SwapCollector struct{}

func (c *SwapCollector) Name() string { return "swap" }

func (c *SwapCollector) Collect(_ context.Context) (map[string]any, error) {
	f, err := os.Open("/proc/swaps")
	if err != nil {
		return map[string]any{
			"count":   0,
			"devices": []any{},
		}, nil
	}
	defer f.Close()

	var devices []any
	scanner := bufio.NewScanner(f)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue // skip header
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		size, _ := strconv.ParseInt(fields[2], 10, 64)
		used, _ := strconv.ParseInt(fields[3], 10, 64)
		pri, _ := strconv.Atoi(fields[4])
		devices = append(devices, map[string]any{
			"path":     fields[0],
			"type":     fields[1],
			"size_kb":  size,
			"used_kb":  used,
			"priority": pri,
		})
	}

	if devices == nil {
		devices = []any{}
	}

	return map[string]any{
		"count":   len(devices),
		"devices": devices,
	}, nil
}
