// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package traits

import (
	"context"

	"github.com/shirou/gopsutil/v4/host"
)

// VirtualCollector detects virtualization: whether the thrall is a VM or
// container, the hypervisor/runtime system, and the role (guest/host).
type VirtualCollector struct{}

func (c *VirtualCollector) Name() string { return "virtual" }

func (c *VirtualCollector) Collect(_ context.Context) (map[string]any, error) {
	system, role, err := host.Virtualization()
	if err != nil {
		return nil, err
	}

	isVirtual := system != ""

	return map[string]any{
		"is_virtual": isVirtual,
		"system":     system,
		"role":       role,
	}, nil
}
