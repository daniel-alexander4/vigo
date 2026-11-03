// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package sandgorgon

import (
	"context"
	"fmt"
	"log/slog"

	"git.alexander4.io/vigo/server/db"
)

// ProvisionRequest contains parameters for provisioning an asset.
type ProvisionRequest struct {
	AssetID     string // asset to provision
	ImageName   string // registered image name
	Hostname    string // hostname to assign (overrides asset hostname if set)
	EnrollToken string // optional Vigo enrollment token
}

// ProvisionResult contains the outcome of a provisioning operation.
type ProvisionResult struct {
	AssetID    string `json:"asset_id"`
	Hostname   string `json:"hostname"`
	ImageName  string `json:"image_name"`
	IPXEScript string `json:"ipxe_script"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	ManualBoot bool   `json:"manual_boot"` // true when no BMC — operator must PXE boot manually
}

// Provision sets up an asset for PXE boot:
// 1. Validate asset state (must be commissioned or available)
// 2. Look up the image
// 3. Set one-time PXE boot via Redfish
// 4. Generate iPXE script
// 5. Power cycle the machine
// 6. Transition to provisioned state
func (m *Manager) Provision(ctx context.Context, req ProvisionRequest, serverAddr string) (*ProvisionResult, error) {
	// Load asset.
	asset, err := m.queries.GetSandgorgonAsset(ctx, req.AssetID)
	if err != nil {
		return nil, fmt.Errorf("asset not found: %w", err)
	}

	// Validate transition.
	if err := ValidateTransition(asset.LifecycleState, StateProvisioned); err != nil {
		// Also allow direct available → provisioned via commissioned.
		if asset.LifecycleState == StateAvailable {
			if err2 := ValidateTransition(StateCommissioned, StateProvisioned); err2 != nil {
				return nil, fmt.Errorf("asset %s: %w", asset.Hostname, err)
			}
		} else {
			return nil, fmt.Errorf("asset %s: %w", asset.Hostname, err)
		}
	}

	// Resolve hostname.
	hostname := req.Hostname
	if hostname == "" {
		hostname = asset.Hostname
	}
	if hostname == "" {
		return nil, fmt.Errorf("hostname is required for provisioning")
	}

	// Look up image.
	image, err := m.queries.GetSandgorgonImageByName(ctx, req.ImageName)
	if err != nil {
		return nil, fmt.Errorf("image %q not found: %w", req.ImageName, err)
	}

	slog.Info("starting provisioning", "asset", hostname, "image", image.Name, "bmc", asset.BmcAddress)

	result := &ProvisionResult{
		AssetID:   asset.ID,
		Hostname:  hostname,
		ImageName: image.Name,
	}

	// Generate iPXE script.
	script, err := GenerateIPXEScript(image, serverAddr, hostname, req.EnrollToken)
	if err != nil {
		result.Error = fmt.Sprintf("iPXE generation failed: %v", err)
		return result, err
	}
	result.IPXEScript = script

	// BMC-automated or manual boot path.
	if asset.BmcAddress != "" {
		// Rack server path: automated PXE boot via Redfish.
		rf := NewRedfishClient(asset.BmcAddress, asset.BmcUsername, asset.BmcPassword)
		if err := rf.Ping(ctx); err != nil {
			m.recordEvent(ctx, asset.ID, "bmc_unreachable", err.Error())
			result.Error = fmt.Sprintf("BMC unreachable: %v", err)
			return result, fmt.Errorf("BMC unreachable at %s: %w", asset.BmcAddress, err)
		}
		if err := rf.SetBootOverridePXE(ctx); err != nil {
			m.recordEvent(ctx, asset.ID, "pxe_boot_override_failed", err.Error())
			result.Error = fmt.Sprintf("PXE boot override failed: %v", err)
			return result, fmt.Errorf("setting PXE boot override: %w", err)
		}
		if err := rf.PowerCycle(ctx); err != nil {
			m.recordEvent(ctx, asset.ID, "power_cycle_failed", err.Error())
			result.Error = fmt.Sprintf("power cycle failed: %v", err)
			return result, fmt.Errorf("power cycling: %w", err)
		}
	} else {
		// Home system path: no BMC — operator boots into PXE manually.
		result.ManualBoot = true
		slog.Info("no BMC — manual PXE boot required", "asset", hostname)
	}

	// Transition state.
	if err := m.queries.UpdateSandgorgonAssetState(ctx, db.UpdateSandgorgonAssetStateParams{
		LifecycleState: StateProvisioned,
		ID:             asset.ID,
	}); err != nil {
		slog.Error("failed to update asset state", "error", err)
	}

	m.recordEvent(ctx, asset.ID, "provisioning_started", fmt.Sprintf("image: %s, hostname: %s", image.Name, hostname))
	result.Status = "provisioning"

	slog.Info("provisioning initiated", "asset", hostname, "image", image.Name)
	return result, nil
}
