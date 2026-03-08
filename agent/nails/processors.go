package nails

import (
	"context"

	"github.com/shirou/gopsutil/v4/cpu"
)

// ProcessorsCollector gathers detailed CPU nails: model name, vendor, physical
// cores, logical cores, and clock speed in MHz.
type ProcessorsCollector struct{}

func (c *ProcessorsCollector) Name() string { return "processors" }

func (c *ProcessorsCollector) Collect(_ context.Context) (map[string]any, error) {
	infos, err := cpu.Info()
	if err != nil {
		return nil, err
	}

	physicalCores, err := cpu.Counts(false)
	if err != nil {
		return nil, err
	}

	logicalCores, err := cpu.Counts(true)
	if err != nil {
		return nil, err
	}

	result := map[string]any{
		"physical_cores": physicalCores,
		"logical_cores":  logicalCores,
	}

	if len(infos) > 0 {
		result["model"] = infos[0].ModelName
		result["vendor"] = infos[0].VendorID
		result["mhz"] = infos[0].Mhz
	}

	return result, nil
}
