// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
)

// ExportHandler serves compliance framework export endpoints (SIEM, CMDB, OSCAL).
type ExportHandler struct {
	queries       *db.Queries
	fleetIndex    *fleet.FleetIndex
	configStore   *config.ConfigStore
	reportHandler *ReportHandler
	exportCfg     config.ExportSection
}

// NewExportHandler creates an ExportHandler with the given dependencies.
// reportHandler is the honest per-control compliance producer the OSCAL
// export routes through — it must be non-nil whenever OSCAL is enabled.
func NewExportHandler(queries *db.Queries, fleetIndex *fleet.FleetIndex, configStore *config.ConfigStore, reportHandler *ReportHandler, exportCfg config.ExportSection) *ExportHandler {
	return &ExportHandler{
		queries:       queries,
		fleetIndex:    fleetIndex,
		configStore:   configStore,
		reportHandler: reportHandler,
		exportCfg:     exportCfg,
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
	Timestamp       string `json:"timestamp"`
	Hostname        string `json:"hostname"`
	EnvoyID         string `json:"envoy_id"`
	RunID           string `json:"run_id"`
	ConfigcrateName string `json:"configcrate_name"`
	ResourceName    string `json:"resource_name"`
	ResourceType    string `json:"resource_type"`
	Action          string `json:"action"`
	Changed         bool   `json:"changed"`
	Error           string `json:"error,omitempty"`
	DurationMs      int64  `json:"duration_ms"`
	RunStatus       string `json:"run_status"`
	Source          string `json:"source"`
}

// CMDBRecord is a CMDB-consumable configuration item for a envoy.
type CMDBRecord struct {
	Hostname          string `json:"hostname"`
	IP                string `json:"ip"`
	EnvoyID           string `json:"envoy_id"`
	OS                string `json:"os,omitempty"`
	OSVersion         string `json:"os_version,omitempty"`
	Architecture      string `json:"architecture,omitempty"`
	Kernel            string `json:"kernel,omitempty"`
	CPUCount          string `json:"cpu_count,omitempty"`
	MemoryTotal       string `json:"memory_total,omitempty"`
	ConvergenceStatus string `json:"compliance_status"`
	LastSeen          string `json:"last_seen"`
	Environment       string `json:"environment,omitempty"`
	EnrolledAt        string `json:"enrolled_at"`
	Uptime            string `json:"uptime,omitempty"`
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
			writeError(w, http.StatusBadRequest, "invalid 'since' parameter: expected RFC3339 format")
			return
		}
		since = parsed
	}

	// Parse ?limit= (default: 1000, max: 10000).
	limit := int64(defaultSIEMLimit)
	if l := r.URL.Query().Get("limit"); l != "" {
		parsed, err := strconv.ParseInt(l, 10, 64)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, "invalid 'limit' parameter: expected positive integer")
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
			writeError(w, http.StatusInternalServerError, "query failed")
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
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		events = make([]SIEMEvent, 0, len(rows))
		for _, row := range rows {
			events = append(events, siemEventFromRow(row))
		}
	}

	writeJSON(w, r, http.StatusOK, events)
}

func siemEventFromRow(row db.ListRunResultsSinceRow) SIEMEvent {
	e := SIEMEvent{
		Timestamp:       row.CreatedAt.Format(time.RFC3339),
		Hostname:        row.Hostname,
		EnvoyID:         row.EnvoyID,
		RunID:           row.RunID,
		ConfigcrateName: row.ConfigcrateName,
		ResourceName:    row.ResourceName,
		ResourceType:    row.ResourceType,
		Action:          row.ActionTaken,
		Changed:         row.Changed != 0,
		DurationMs:      row.DurationMs,
		RunStatus:       row.RunStatus,
		Source:          "vigo",
	}
	if row.ErrorMessage.Valid {
		e.Error = row.ErrorMessage.String
	}
	return e
}

func siemEventFromByEnvoyRow(row db.ListRunResultsSinceByEnvoyRow) SIEMEvent {
	e := SIEMEvent{
		Timestamp:       row.CreatedAt.Format(time.RFC3339),
		Hostname:        row.Hostname,
		EnvoyID:         row.EnvoyID,
		RunID:           row.RunID,
		ConfigcrateName: row.ConfigcrateName,
		ResourceName:    row.ResourceName,
		ResourceType:    row.ResourceType,
		Action:          row.ActionTaken,
		Changed:         row.Changed != 0,
		DurationMs:      row.DurationMs,
		RunStatus:       row.RunStatus,
		Source:          "vigo",
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
			Hostname:          ts.Hostname,
			IP:                ts.IP,
			EnvoyID:           ts.ID,
			OS:                ts.Traits["os.family"],
			OSVersion:         ts.Traits["os.version"],
			Architecture:      ts.Traits["os.arch"],
			Kernel:            ts.Traits["kernel.version"],
			CPUCount:          ts.Traits["hardware.cpu_count"],
			MemoryTotal:       ts.Traits["memory.total"],
			ConvergenceStatus: ts.ConvergenceStatus,
			Environment:       env,
			Uptime:            ts.Traits["uptime.seconds"],
		}
		if !ts.LastSeen.IsZero() {
			rec.LastSeen = ts.LastSeen.Format(time.RFC3339)
		}
		if !ts.EnrolledAt.IsZero() {
			rec.EnrolledAt = ts.EnrolledAt.Format(time.RFC3339)
		}

		records = append(records, rec)
	}

	writeJSON(w, r, http.StatusOK, records)
}

// oscalStatusNS namespaces the Vigo-specific props we hang off each
// OSCAL finding — the real per-control status and fleet coverage that
// don't fit OSCAL's binary satisfied/not-satisfied state field.
const oscalStatusNS = "https://vigo.dev/ns/oscal"

func (h *ExportHandler) handleOSCAL(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC().Format(time.RFC3339)

	// Route through the honest per-control compliance producer rather
	// than a hardcoded all-satisfied table. BuildFrameworkReport("compliance")
	// is the HIPAA framework — the same real, fleet-scored control set the
	// PDF report and audit bundle emit. Every control's OSCAL state reflects
	// the actual fleet posture.
	report, err := h.reportHandler.BuildFrameworkReport(r, "compliance")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build compliance report")
		return
	}

	type oscalProp struct {
		Name  string `json:"name"`
		Value string `json:"value"`
		NS    string `json:"ns,omitempty"`
	}
	type oscalFinding struct {
		UUID        string         `json:"uuid"`
		Title       string         `json:"title"`
		Description string         `json:"description"`
		Props       []oscalProp    `json:"props,omitempty"`
		Target      map[string]any `json:"target"`
	}

	findings := make([]oscalFinding, len(report.Controls))
	for i, c := range report.Controls {
		// Conservative mapping: only a genuinely satisfied control maps to
		// the OSCAL "satisfied" state. partial / waived / not_satisfied all
		// map to "not-satisfied" so a reader parsing only the state field
		// never over-reads Vigo's posture. The real status is preserved in
		// the vigo-status prop for consumers that want the finer grade.
		state := "not-satisfied"
		if c.Status == "satisfied" {
			state = "satisfied"
		}
		findings[i] = oscalFinding{
			UUID:        uuid.NewString(),
			Title:       c.ControlID + " " + c.Title,
			Description: c.Evidence,
			Props: []oscalProp{
				{Name: "vigo-status", Value: c.Status, NS: oscalStatusNS},
				{Name: "vigo-scope", Value: c.Scope, NS: oscalStatusNS},
				{Name: "vigo-fleet-percent", Value: strconv.FormatFloat(c.FleetPercent, 'f', 1, 64), NS: oscalStatusNS},
			},
			Target: map[string]any{
				"type":      "control",
				"target-id": c.ControlID,
				"title":     c.Title,
				"status": map[string]string{
					"state": state,
				},
			},
		}
	}

	oscal := map[string]any{
		"assessment-results": map[string]any{
			"uuid": uuid.NewString(),
			"metadata": map[string]any{
				"title":         "Vigo HIPAA Assessment Results",
				"last-modified": now,
				"version":       report.Version,
				"oscal-version": "1.1.2",
			},
			"results": []map[string]any{
				{
					"uuid":        uuid.NewString(),
					"title":       "Vigo HIPAA Compliance Assessment",
					"description": "Automated assessment of HIPAA Security Rule technical controls implemented by Vigo, scored against the live fleet.",
					"start":       now,
					"reviewed-controls": map[string]any{
						"control-selections": []map[string]any{
							{"description": "HIPAA Security Rule 45 CFR 164.302–318"},
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
