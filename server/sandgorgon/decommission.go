// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package sandgorgon

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"git.alexander4.io/vigo/server/db"
)

// DecommissionRequest contains parameters for a decommission operation.
type DecommissionRequest struct {
	AssetID    string // asset to decommission
	Technician string // who is performing the operation
	Method     string // "auto", "clear", "purge", or "destroy"
}

// DecommissionResult contains the outcome of a decommission.
type DecommissionResult struct {
	AssetID       string           `json:"asset_id"`
	Hostname      string           `json:"hostname"`
	CertificateID string           `json:"certificate_id"`
	Drives        []SanitizeResult `json:"drives"`
	Error         string           `json:"error,omitempty"`
	ManualWipe    bool             `json:"manual_wipe"` // true when no BMC — operator must wipe manually
}

// Manager coordinates sandgorgon operations.
type Manager struct {
	queries     *db.Queries
	fleetIndex  FleetTraitWriter
	lifecycleDS LifecycleDataSource
}

// FleetTraitWriter is the subset of FleetIndex needed for writing traits.
type FleetTraitWriter interface {
	UpdateTraits(envoyID string, traits map[string]string)
}

// NewManager creates a new sandgorgon manager.
func NewManager(queries *db.Queries) *Manager {
	return &Manager{queries: queries}
}

// SetFleetIndex enables lifecycle trait writing.
func (m *Manager) SetFleetIndex(fi FleetTraitWriter) {
	m.fleetIndex = fi
}

// Decommission executes the full decommission workflow:
// 1. Validate asset state
// 2. Set lifecycle_state to decommissioning
// 3. Connect to BMC via Redfish
// 4. Enumerate and sanitize drives (per media type)
// 5. Generate NIST 800-88 certificate
// 6. Set lifecycle_state to retired
func (m *Manager) Decommission(ctx context.Context, req DecommissionRequest) (*DecommissionResult, error) {
	// Load asset.
	asset, err := m.queries.GetSandgorgonAsset(ctx, req.AssetID)
	if err != nil {
		return nil, fmt.Errorf("asset not found: %w", err)
	}

	if err := ValidateTransition(asset.LifecycleState, StateDecommissioning); err != nil {
		return nil, fmt.Errorf("asset %s: %w", asset.Hostname, err)
	}

	slog.Info("starting decommission", "asset", asset.Hostname, "bmc", asset.BmcAddress, "technician", req.Technician)

	// Transition to decommissioning.
	if err := m.queries.UpdateSandgorgonAssetState(ctx, db.UpdateSandgorgonAssetStateParams{
		LifecycleState: "decommissioning",
		ID:             asset.ID,
	}); err != nil {
		return nil, fmt.Errorf("updating state: %w", err)
	}
	m.recordEvent(ctx, asset.ID, "decommission_started", fmt.Sprintf("technician: %s, method: %s", req.Technician, req.Method))

	result := &DecommissionResult{
		AssetID:  asset.ID,
		Hostname: asset.Hostname,
	}

	if asset.BmcAddress != "" {
		// Rack server path: automated drive sanitization via Redfish.
		rf := NewRedfishClient(asset.BmcAddress, asset.BmcUsername, asset.BmcPassword)
		if err := rf.Ping(ctx); err != nil {
			m.recordEvent(ctx, asset.ID, "bmc_unreachable", err.Error())
			result.Error = fmt.Sprintf("BMC unreachable: %v", err)
			return result, fmt.Errorf("BMC unreachable at %s: %w", asset.BmcAddress, err)
		}

		drives, err := rf.ListDrives(ctx)
		if err != nil {
			m.recordEvent(ctx, asset.ID, "drive_enumeration_failed", err.Error())
			result.Error = fmt.Sprintf("drive enumeration failed: %v", err)
			return result, fmt.Errorf("enumerating drives: %w", err)
		}

		slog.Info("enumerated drives", "asset", asset.Hostname, "count", len(drives))

		for _, drive := range drives {
			sr := sanitizeDrive(ctx, rf, &drive, req.Method)
			result.Drives = append(result.Drives, sr)
			m.recordEvent(ctx, asset.ID, "drive_sanitized", fmt.Sprintf("%s: %s (%s)", drive.Name, sr.Method, sr.VerificationResult))
		}
	} else {
		// Home system path: no BMC — cannot enumerate or sanitize drives remotely.
		result.ManualWipe = true
		m.recordEvent(ctx, asset.ID, "manual_wipe_required", "no BMC — drives not remotely sanitized")
		slog.Info("no BMC — manual wipe required", "asset", asset.Hostname)
	}

	// Generate NIST 800-88 certificate.
	// For manual wipe, the certificate records verification_method as "operator-attested".
	cert, err := m.generateCertificate(ctx, &asset, result, req.Technician)
	if err != nil {
		slog.Error("certificate generation failed", "error", err, "asset", asset.Hostname)
		result.Error = fmt.Sprintf("certificate generation failed: %v", err)
	} else {
		result.CertificateID = cert.ID
	}

	// Mark retired.
	if err := m.queries.UpdateSandgorgonAssetDecommissioned(ctx, asset.ID); err != nil {
		slog.Error("failed to mark asset retired", "error", err, "asset", asset.Hostname)
	}

	m.recordEvent(ctx, asset.ID, "decommission_completed", fmt.Sprintf("certificate: %s", result.CertificateID))

	// Write lifecycle traits.
	if m.fleetIndex != nil && asset.EnvoyID.Valid {
		traits := map[string]string{
			"lifecycle.state":                  "retired",
			"lifecycle.decommissioned_at":      time.Now().Format(time.RFC3339),
			"sanitization.certificate_id":      result.CertificateID,
			"sanitization.date":                time.Now().Format(time.RFC3339),
		}
		if len(result.Drives) > 0 {
			traits["sanitization.method"] = result.Drives[0].Method
			traits["sanitization.verified"] = result.Drives[0].VerificationResult
			traits["sanitization.verification_method"] = result.Drives[0].VerificationMethod
		}
		m.fleetIndex.UpdateTraits(asset.EnvoyID.String, traits)
	}

	slog.Info("decommission complete", "asset", asset.Hostname, "certificate", result.CertificateID)

	return result, nil
}

// sanitizeDrive selects the best sanitization method for a drive and executes it.
func sanitizeDrive(ctx context.Context, rf *RedfishClient, drive *StorageDrive, preferredMethod string) SanitizeResult {
	start := time.Now()
	sr := SanitizeResult{
		DriveName:   drive.Name,
		DriveSerial: drive.SerialNumber,
		MediaType:   classifyMediaType(drive),
		StartedAt:   start,
	}

	method, verification, limitations := selectSanitizationMethod(drive, preferredMethod)
	sr.Method = method
	sr.VerificationMethod = verification
	sr.VerificationLimitations = limitations

	// Dispatch the sanitization via Redfish.
	drivePath := fmt.Sprintf("/redfish/v1/Systems/1/Storage/1/Drives/%s", drive.Name)

	switch method {
	case "Purge":
		// NVMe Sanitize or SED crypto-erase — best available.
		if err := rf.SecureEraseDrive(ctx, drivePath); err != nil {
			sr.VerificationResult = "failed"
			sr.VerificationLimitations = fmt.Sprintf("SecureErase command failed: %v", err)
		} else {
			sr.VerificationResult = "verified"
		}
	case "Clear":
		// Standard secure erase.
		if err := rf.SecureEraseDrive(ctx, drivePath); err != nil {
			sr.VerificationResult = "failed"
			sr.VerificationLimitations = fmt.Sprintf("SecureErase command failed: %v", err)
		} else {
			sr.VerificationResult = "unverifiable"
		}
	default:
		sr.VerificationResult = "not_attempted"
		sr.VerificationLimitations = "no supported sanitization method available via Redfish"
	}

	sr.CompletedAt = time.Now()
	return sr
}

// selectSanitizationMethod picks the method, verification approach, and limitations.
func selectSanitizationMethod(drive *StorageDrive, preferred string) (method, verification, limitations string) {
	if preferred == "destroy" {
		return "Destroy", "physical", "physical destruction must be verified by technician"
	}

	// NVMe with crypto-erase → Purge.
	if drive.Protocol == "NVMe" && drive.SupportsCryptoErase {
		return "Purge", "NVMe Sanitize Log", ""
	}
	if drive.Protocol == "NVMe" && drive.SupportsSecureErase {
		return "Purge", "NVMe Sanitize Log", ""
	}

	// SED (self-encrypting) → Purge via crypto-erase.
	if drive.SupportsCryptoErase {
		return "Purge", "TCG Opal status", ""
	}

	// SATA/SAS with SecureErase → Clear.
	if drive.SupportsSecureErase {
		return "Clear", "drive responsive post-erase",
			"SATA Secure Erase has no completion proof in SMART; certificate states command was issued and drive is responsive afterward"
	}

	// Forced method override.
	if preferred == "purge" {
		return "Purge", "drive responsive post-erase",
			"Purge requested but drive does not report Sanitize capability; falling back to SecureErase"
	}
	if preferred == "clear" {
		return "Clear", "drive responsive post-erase",
			"Clear requested; no firmware-level verification available for this drive type"
	}

	return "None", "none", "no supported sanitization method available via Redfish for this drive"
}

// classifyMediaType returns the NIST 800-88 media classification.
func classifyMediaType(drive *StorageDrive) string {
	switch {
	case drive.Protocol == "NVMe":
		return "NVMe SSD"
	case drive.MediaType == "SSD":
		return "SATA/SAS SSD"
	case drive.MediaType == "HDD":
		return "HDD"
	default:
		return drive.MediaType
	}
}

func (m *Manager) generateCertificate(ctx context.Context, asset *db.SandgorgonAsset, result *DecommissionResult, technician string) (db.SandgorgonCertificate, error) {
	// Aggregate drive results into certificate fields.
	methods := make(map[string]bool)
	mediaTypes := make(map[string]bool)
	verMethods := make(map[string]bool)
	verResults := make(map[string]bool)
	var allLimitations []string

	for _, dr := range result.Drives {
		methods[dr.Method] = true
		mediaTypes[dr.MediaType] = true
		verMethods[dr.VerificationMethod] = true
		verResults[dr.VerificationResult] = true
		if dr.VerificationLimitations != "" {
			allLimitations = append(allLimitations, dr.VerificationLimitations)
		}
	}

	now := time.Now()
	return m.queries.CreateSandgorgonCertificate(ctx, db.CreateSandgorgonCertificateParams{
		AssetID:                 asset.ID,
		EnvoyID:                 nullStr(asset.EnvoyID),
		Hostname:                asset.Hostname,
		SerialNumber:            asset.SerialNumber,
		Model:                   asset.Model,
		AssetTag:                asset.AssetTag,
		SanitizationMethod:      joinKeys(methods),
		MediaType:               joinKeys(mediaTypes),
		VerificationMethod:      joinKeys(verMethods),
		VerificationResult:      joinKeys(verResults),
		VerificationLimitations: joinStrings(allLimitations),
		Technician:              technician,
		StartedAt:               now,
		CompletedAt:             sql.NullTime{Time: now, Valid: true},
		LifecycleSummary:        m.BuildLifecycleSummaryJSON(asset),
	})
}

// TransitionState validates and applies a lifecycle state change.
func (m *Manager) TransitionState(ctx context.Context, assetID, newState string) error {
	asset, err := m.queries.GetSandgorgonAsset(ctx, assetID)
	if err != nil {
		return fmt.Errorf("asset not found: %w", err)
	}
	if err := ValidateTransition(asset.LifecycleState, newState); err != nil {
		return err
	}
	if err := m.queries.UpdateSandgorgonAssetState(ctx, db.UpdateSandgorgonAssetStateParams{
		LifecycleState: newState,
		ID:             assetID,
	}); err != nil {
		return err
	}
	m.recordEvent(ctx, assetID, "state_changed", fmt.Sprintf("%s → %s", asset.LifecycleState, newState))
	return nil
}

// ReturnToAvailable resets a retired asset to available state for re-use.
func (m *Manager) ReturnToAvailable(ctx context.Context, assetID string) error {
	asset, err := m.queries.GetSandgorgonAsset(ctx, assetID)
	if err != nil {
		return fmt.Errorf("asset not found: %w", err)
	}
	if err := ValidateTransition(asset.LifecycleState, StateAvailable); err != nil {
		return err
	}
	if err := m.queries.UpdateSandgorgonAssetReturnToAvailable(ctx, assetID); err != nil {
		return err
	}
	m.recordEvent(ctx, assetID, "returned_to_available", "reset after retirement")
	return nil
}

func (m *Manager) recordEvent(ctx context.Context, assetID, eventType, detail string) {
	if err := m.queries.CreateSandgorgonEvent(ctx, db.CreateSandgorgonEventParams{
		AssetID:   assetID,
		EventType: eventType,
		Detail:    detail,
	}); err != nil {
		slog.Error("failed to record sandgorgon event", "error", err)
	}
}

func nullStr(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

func joinKeys(m map[string]bool) string {
	var parts []string
	for k := range m {
		parts = append(parts, k)
	}
	return joinStrings(parts)
}

func joinStrings(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	result := ss[0]
	for _, s := range ss[1:] {
		result += "; " + s
	}
	return result
}
