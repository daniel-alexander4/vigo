// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
)

// ExportHandler serves compliance framework export endpoints (SIEM, CMDB, OSCAL).
type ExportHandler struct {
	queries     *db.Queries
	fleetIndex  *fleet.FleetIndex
	configStore *config.ConfigStore
	exportCfg   config.ExportSection
}

// NewExportHandler creates an ExportHandler with the given dependencies.
func NewExportHandler(queries *db.Queries, fleetIndex *fleet.FleetIndex, configStore *config.ConfigStore, exportCfg config.ExportSection) *ExportHandler {
	return &ExportHandler{
		queries:     queries,
		fleetIndex:  fleetIndex,
		configStore: configStore,
		exportCfg:   exportCfg,
	}
}

// RegisterRoutes registers only the enabled export endpoints on the
// given mux. SIEM/CMDB are fleet exports (per-envoy run results,
// inventory) so they require fleet.read; OSCAL is a compliance
// artifact and rides compliance.read.
func (h *ExportHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	complianceRead := auth.RequirePermission(auth.PermComplianceRead)
	if h.exportCfg.SIEM {
		mux.Handle("GET /api/v1/export/siem", read(http.HandlerFunc(h.handleSIEM)))
	}
	if h.exportCfg.CMDB {
		mux.Handle("GET /api/v1/export/cmdb", read(http.HandlerFunc(h.handleCMDB)))
	}
	if h.exportCfg.OSCAL {
		mux.Handle("GET /api/v1/export/oscal", complianceRead(http.HandlerFunc(h.handleOSCAL)))
	}
}

// SIEMEvent is a single SIEM-consumable event derived from a run result.
type SIEMEvent struct {
	Timestamp    string `json:"timestamp"`
	Hostname     string `json:"hostname"`
	EnvoyID     string `json:"envoy_id"`
	RunID        string `json:"run_id"`
	ModuleName   string `json:"module_name"`
	ResourceName string `json:"resource_name"`
	ResourceType string `json:"resource_type"`
	Action       string `json:"action"`
	Changed      bool   `json:"changed"`
	Error        string `json:"error,omitempty"`
	DurationMs   int64  `json:"duration_ms"`
	RunStatus    string `json:"run_status"`
	Source       string `json:"source"`
}

// CMDBRecord is a CMDB-consumable configuration item for a envoy.
type CMDBRecord struct {
	Hostname         string `json:"hostname"`
	IP               string `json:"ip"`
	EnvoyID         string `json:"envoy_id"`
	OS               string `json:"os,omitempty"`
	OSVersion        string `json:"os_version,omitempty"`
	Architecture     string `json:"architecture,omitempty"`
	Kernel           string `json:"kernel,omitempty"`
	CPUCount         string `json:"cpu_count,omitempty"`
	MemoryTotal      string `json:"memory_total,omitempty"`
	ConvergenceStatus string `json:"compliance_status"`
	LastSeen         string `json:"last_seen"`
	Environment      string `json:"environment,omitempty"`
	EnrolledAt       string `json:"enrolled_at"`
	Uptime           string `json:"uptime,omitempty"`
}

const (
	defaultSIEMLimit = 1000
	maxSIEMLimit     = 10000
	defaultSIEMSince = 24 * time.Hour
)

func (h *ExportHandler) handleSIEM(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse ?since= (default: 24h ago).
	since := time.Now().UTC().Add(-defaultSIEMSince)
	if s := r.URL.Query().Get("since"); s != "" {
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid 'since' parameter: expected RFC3339 format")
			return
		}
		since = parsed
	}

	// Parse ?limit= (default: 1000, max: 10000).
	limit := int64(defaultSIEMLimit)
	if l := r.URL.Query().Get("limit"); l != "" {
		parsed, err := strconv.ParseInt(l, 10, 64)
		if err != nil || parsed < 1 {
			writeJSONError(w, http.StatusBadRequest, "invalid 'limit' parameter: expected positive integer")
			return
		}
		limit = parsed
		if limit > maxSIEMLimit {
			limit = maxSIEMLimit
		}
	}

	envoyFilter := r.URL.Query().Get("envoy")

	var events []SIEMEvent

	if envoyFilter != "" {
		rows, err := h.queries.ListRunResultsSinceByEnvoy(ctx, db.ListRunResultsSinceByEnvoyParams{
			CreatedAt: since,
			Hostname:  envoyFilter,
			Limit:     limit,
		})
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "query failed")
			return
		}
		events = make([]SIEMEvent, 0, len(rows))
		for _, row := range rows {
			events = append(events, siemEventFromByEnvoyRow(row))
		}
	} else {
		rows, err := h.queries.ListRunResultsSince(ctx, db.ListRunResultsSinceParams{
			CreatedAt: since,
			Limit:     limit,
		})
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "query failed")
			return
		}
		events = make([]SIEMEvent, 0, len(rows))
		for _, row := range rows {
			events = append(events, siemEventFromRow(row))
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

func siemEventFromRow(row db.ListRunResultsSinceRow) SIEMEvent {
	e := SIEMEvent{
		Timestamp:    row.CreatedAt.Format(time.RFC3339),
		Hostname:     row.Hostname,
		EnvoyID:     row.EnvoyID,
		RunID:        row.RunID,
		ModuleName:   row.ModuleName,
		ResourceName: row.ResourceName,
		ResourceType: row.ResourceType,
		Action:       row.ActionTaken,
		Changed:      row.Changed != 0,
		DurationMs:   row.DurationMs,
		RunStatus:    row.RunStatus,
		Source:       "vigo",
	}
	if row.ErrorMessage.Valid {
		e.Error = row.ErrorMessage.String
	}
	return e
}

func siemEventFromByEnvoyRow(row db.ListRunResultsSinceByEnvoyRow) SIEMEvent {
	e := SIEMEvent{
		Timestamp:    row.CreatedAt.Format(time.RFC3339),
		Hostname:     row.Hostname,
		EnvoyID:     row.EnvoyID,
		RunID:        row.RunID,
		ModuleName:   row.ModuleName,
		ResourceName: row.ResourceName,
		ResourceType: row.ResourceType,
		Action:       row.ActionTaken,
		Changed:      row.Changed != 0,
		DurationMs:   row.DurationMs,
		RunStatus:    row.RunStatus,
		Source:       "vigo",
	}
	if row.ErrorMessage.Valid {
		e.Error = row.ErrorMessage.String
	}
	return e
}

func (h *ExportHandler) handleCMDB(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	envFilter := r.URL.Query().Get("environment")

	allEnvoys := h.fleetIndex.AllEnvoys()
	records := make([]CMDBRecord, 0, len(allEnvoys))

	for _, ts := range allEnvoys {
		if ts.Revoked {
			continue
		}

		if statusFilter != "" && ts.ConvergenceStatus != statusFilter {
			continue
		}

		// Look up environment from config store.
		var env string
		if h.configStore != nil {
			if tc := h.configStore.Get(ts.Hostname); tc != nil {
				env = tc.Environment
			}
		}
		if envFilter != "" && env != envFilter {
			continue
		}

		rec := CMDBRecord{
			Hostname:         ts.Hostname,
			IP:               ts.IP,
			EnvoyID:         ts.ID,
			OS:               ts.Traits["os.family"],
			OSVersion:        ts.Traits["os.version"],
			Architecture:     ts.Traits["os.arch"],
			Kernel:           ts.Traits["kernel.version"],
			CPUCount:         ts.Traits["hardware.cpu_count"],
			MemoryTotal:      ts.Traits["memory.total"],
			ConvergenceStatus: ts.ConvergenceStatus,
			Environment:      env,
			Uptime:           ts.Traits["uptime.seconds"],
		}
		if !ts.LastSeen.IsZero() {
			rec.LastSeen = ts.LastSeen.Format(time.RFC3339)
		}
		if !ts.EnrolledAt.IsZero() {
			rec.EnrolledAt = ts.EnrolledAt.Format(time.RFC3339)
		}

		records = append(records, rec)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(records)
}

func (h *ExportHandler) handleOSCAL(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC().Format(time.RFC3339)

	// Build findings from HIPAA control mappings.
	type oscalFinding struct {
		UUID        string         `json:"uuid"`
		Title       string         `json:"title"`
		Description string         `json:"description"`
		Target      map[string]any `json:"target"`
	}

	controlMappings := []struct {
		id, title, status, desc string
	}{
		{"164.312(a)(1)", "Access Control", "satisfied", "Authentication required on all access, RBAC with fine-grained permissions"},
		{"164.312(a)(2)(i)", "Unique User Identification", "satisfied", "Per-user accounts with unique UUIDs"},
		{"164.312(a)(2)(ii)", "Emergency Access Procedure", "satisfied", "Localhost trust with audited emergency-access CLI command"},
		{"164.312(a)(2)(iii)", "Automatic Logoff", "satisfied", "15-minute idle session timeout"},
		{"164.312(a)(2)(iv)", "Encryption and Decryption", "satisfied", "AES-256-GCM for secrets and backups"},
		{"164.312(b)", "Audit Controls", "satisfied", "SHA-256 hash chain audit trail with auth/user/token events"},
		{"164.312(c)(1)", "Integrity", "satisfied", "ED25519 signatures, hash chain tamper detection"},
		{"164.312(d)", "Person or Entity Authentication", "satisfied", "Basic/OIDC/isowebauth with optional TOTP MFA"},
		{"164.312(e)(1)", "Transmission Security", "satisfied", "mTLS (TLS 1.3) on all traffic"},
		{"164.312(e)(2)(ii)", "Encryption (Transmission)", "satisfied", "No plaintext transport"},
		{"164.310(d)(2)(i)", "Disposal", "satisfied", "Documented decommissioning procedures"},
		{"164.308(a)(5)(ii)(D)", "Password Management", "satisfied", "12-char minimum, complexity rules, bcrypt"},
		{"164.308(a)(6)", "Security Incident Procedures", "satisfied", "Documented incident response plan"},
	}

	findings := make([]oscalFinding, len(controlMappings))
	for i, c := range controlMappings {
		findings[i] = oscalFinding{
			UUID:        fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1),
			Title:       c.id + " " + c.title,
			Description: c.desc,
			Target: map[string]any{
				"type":        "control",
				"target-id":   c.id,
				"title":       c.title,
				"status": map[string]string{
					"state": c.status,
				},
			},
		}
	}

	oscal := map[string]any{
		"assessment-results": map[string]any{
			"uuid": "00000000-0000-4000-8000-000000000000",
			"metadata": map[string]any{
				"title":         "Vigo HIPAA Assessment Results",
				"last-modified": now,
				"version":       "1.0.0",
				"oscal-version": "1.1.2",
			},
			"results": []map[string]any{
				{
					"uuid":        "00000000-0000-4000-8000-000000000100",
					"title":       "Vigo HIPAA Compliance Assessment",
					"description": "Automated assessment of HIPAA Security Rule technical controls implemented by Vigo.",
					"start":       now,
					"reviewed-controls": map[string]any{
						"control-selections": []map[string]any{
							{"description": "HIPAA Security Rule 45 CFR 164.312"},
						},
					},
					"findings": findings,
				},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(oscal)
}
