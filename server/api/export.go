// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/fleet"
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

// RegisterRoutes registers only the enabled export endpoints on the given mux.
func (h *ExportHandler) RegisterRoutes(mux *http.ServeMux) {
	if h.exportCfg.SIEM {
		mux.HandleFunc("GET /api/v1/export/siem", h.handleSIEM)
	}
	if h.exportCfg.CMDB {
		mux.HandleFunc("GET /api/v1/export/cmdb", h.handleCMDB)
	}
	if h.exportCfg.OSCAL {
		mux.HandleFunc("GET /api/v1/export/oscal", h.handleOSCAL)
	}
}

// SIEMEvent is a single SIEM-consumable event derived from a run result.
type SIEMEvent struct {
	Timestamp    string `json:"timestamp"`
	Hostname     string `json:"hostname"`
	ThrallID     string `json:"thrall_id"`
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

// CMDBRecord is a CMDB-consumable configuration item for a thrall.
type CMDBRecord struct {
	Hostname         string `json:"hostname"`
	IP               string `json:"ip"`
	ThrallID         string `json:"thrall_id"`
	OS               string `json:"os,omitempty"`
	OSVersion        string `json:"os_version,omitempty"`
	Architecture     string `json:"architecture,omitempty"`
	Kernel           string `json:"kernel,omitempty"`
	CPUCount         string `json:"cpu_count,omitempty"`
	MemoryTotal      string `json:"memory_total,omitempty"`
	ComplianceStatus string `json:"compliance_status"`
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

	thrallFilter := r.URL.Query().Get("thrall")

	var events []SIEMEvent

	if thrallFilter != "" {
		rows, err := h.queries.ListRunResultsSinceByThrall(ctx, db.ListRunResultsSinceByThrallParams{
			CreatedAt: since,
			Hostname:  thrallFilter,
			Limit:     limit,
		})
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "query failed")
			return
		}
		events = make([]SIEMEvent, 0, len(rows))
		for _, row := range rows {
			events = append(events, siemEventFromByThrallRow(row))
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
		ThrallID:     row.ThrallID,
		RunID:        row.RunID,
		ModuleName:   row.ModuleName,
		ResourceName: row.ResourceName,
		ResourceType: row.ResourceType,
		Action:       row.ActionTaken,
		Changed:      row.Changed != 0,
		DurationMs:   row.DurationMs,
		RunStatus:    row.RunStatus,
		Source:       "hobnob",
	}
	if row.ErrorMessage.Valid {
		e.Error = row.ErrorMessage.String
	}
	return e
}

func siemEventFromByThrallRow(row db.ListRunResultsSinceByThrallRow) SIEMEvent {
	e := SIEMEvent{
		Timestamp:    row.CreatedAt.Format(time.RFC3339),
		Hostname:     row.Hostname,
		ThrallID:     row.ThrallID,
		RunID:        row.RunID,
		ModuleName:   row.ModuleName,
		ResourceName: row.ResourceName,
		ResourceType: row.ResourceType,
		Action:       row.ActionTaken,
		Changed:      row.Changed != 0,
		DurationMs:   row.DurationMs,
		RunStatus:    row.RunStatus,
		Source:       "hobnob",
	}
	if row.ErrorMessage.Valid {
		e.Error = row.ErrorMessage.String
	}
	return e
}

func (h *ExportHandler) handleCMDB(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	envFilter := r.URL.Query().Get("environment")

	allThralls := h.fleetIndex.AllThralls()
	records := make([]CMDBRecord, 0, len(allThralls))

	for _, ts := range allThralls {
		if ts.Revoked {
			continue
		}

		if statusFilter != "" && ts.ComplianceStatus != statusFilter {
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
			ThrallID:         ts.ID,
			OS:               ts.Traits["os.family"],
			OSVersion:        ts.Traits["os.version"],
			Architecture:     ts.Traits["hardware.architecture"],
			Kernel:           ts.Traits["kernel.version"],
			CPUCount:         ts.Traits["hardware.cpu_count"],
			MemoryTotal:      ts.Traits["memory.total"],
			ComplianceStatus: ts.ComplianceStatus,
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
	// Minimal valid OSCAL Assessment Results envelope.
	oscal := map[string]any{
		"assessment-results": map[string]any{
			"uuid": "00000000-0000-4000-8000-000000000000",
			"metadata": map[string]any{
				"title":        "Hobnob Assessment Results",
				"last-modified": time.Now().UTC().Format(time.RFC3339),
				"version":      "1.0.0",
				"oscal-version": "1.1.2",
			},
			"results": []map[string]any{
				{
					"uuid":        "00000000-0000-4000-8000-000000000001",
					"title":       "Hobnob Compliance Data",
					"description": "Stub — Hobnob provides evidence via SIEM and CMDB exports. OSCAL assessment mapping is not yet implemented.",
					"start":       time.Now().UTC().Format(time.RFC3339),
					"reviewed-controls": map[string]any{
						"control-selections": []map[string]any{},
					},
					"findings": []any{},
				},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(oscal)
}
