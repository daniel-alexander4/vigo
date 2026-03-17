// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/hobnob/hobnob/server/audit"
	"github.com/hobnob/hobnob/server/db"
)

// AuditHandler provides a unified audit view across event types.
type AuditHandler struct {
	queries *db.Queries
}

// NewAuditHandler creates an AuditHandler.
func NewAuditHandler(queries *db.Queries) *AuditHandler {
	return &AuditHandler{queries: queries}
}

// RegisterRoutes registers audit endpoints.
func (h *AuditHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/audit", h.ListAuditEvents)
	mux.HandleFunc("GET /api/v1/audit/verify", h.VerifyChain)
}

type auditEvent struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Operator  string `json:"operator"`
	Timestamp string `json:"timestamp"`
	Summary   string `json:"summary"`
}

// ListAuditEvents returns a unified view of auditable events.
func (h *AuditHandler) ListAuditEvents(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	typeFilter := r.URL.Query().Get("type")
	operatorFilter := r.URL.Query().Get("operator")

	var sinceTime time.Time
	if s := r.URL.Query().Get("since"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			sinceTime = time.Now().Add(-d)
		} else if t, err := time.Parse(time.RFC3339, s); err == nil {
			sinceTime = t
		}
	}

	var events []auditEvent

	// Tamper-evident audit entries (hash-chained).
	if typeFilter == "" || typeFilter == "audit_entry" {
		entries, err := h.queries.ListAuditEntries(r.Context(), int64(limit))
		if err == nil {
			for _, e := range entries {
				if !sinceTime.IsZero() && e.Timestamp.Before(sinceTime) {
					continue
				}
				if operatorFilter != "" && e.Actor != operatorFilter {
					continue
				}
				events = append(events, auditEvent{
					ID:        fmt.Sprintf("ae-%d", e.ID),
					Type:      e.EventType,
					Operator:  e.Actor,
					Timestamp: e.Timestamp.Format(time.RFC3339),
					Summary:   e.Summary,
				})
			}
		}
	}

	// Config publishes.
	if typeFilter == "" || typeFilter == "config_publish" {
		publishes, err := h.queries.ListConfigPublishes(r.Context(), int64(limit))
		if err == nil {
			for _, p := range publishes {
				if !sinceTime.IsZero() && p.PublishedAt.Before(sinceTime) {
					continue
				}
				if operatorFilter != "" && p.Operator != operatorFilter {
					continue
				}
				var changed []string
				_ = json.Unmarshal([]byte(p.ChangedFiles), &changed)
				events = append(events, auditEvent{
					ID:        p.ID,
					Type:      "config_publish",
					Operator:  p.Operator,
					Timestamp: p.PublishedAt.Format(time.RFC3339),
					Summary:   strconv.Itoa(len(changed)) + " files changed",
				})
			}
		}
	}

	// Task runs.
	if typeFilter == "" || typeFilter == "task_run" {
		taskRuns, err := h.queries.ListTaskRuns(r.Context(), int64(limit))
		if err == nil {
			for _, tr := range taskRuns {
				if !sinceTime.IsZero() && tr.CreatedAt.Before(sinceTime) {
					continue
				}
				if operatorFilter != "" && tr.Operator != operatorFilter {
					continue
				}
				events = append(events, auditEvent{
					ID:        tr.ID,
					Type:      "task_run",
					Operator:  tr.Operator,
					Timestamp: tr.CreatedAt.Format(time.RFC3339),
					Summary:   tr.Command + " on " + tr.TargetGlob,
				})
			}
		}
	}

	// Workflow runs.
	if typeFilter == "" || typeFilter == "workflow_run" {
		workflowRuns, err := h.queries.ListWorkflowRuns(r.Context(), int64(limit))
		if err == nil {
			for _, wr := range workflowRuns {
				if !sinceTime.IsZero() && wr.CreatedAt.Before(sinceTime) {
					continue
				}
				if operatorFilter != "" && wr.Operator != operatorFilter {
					continue
				}
				events = append(events, auditEvent{
					ID:        wr.ID,
					Type:      "workflow_run",
					Operator:  wr.Operator,
					Timestamp: wr.CreatedAt.Format(time.RFC3339),
					Summary:   wr.WorkflowName + " (" + wr.Status + ")",
				})
			}
		}
	}

	// Sort by timestamp descending.
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp > events[j].Timestamp
	})

	// Apply limit.
	if len(events) > limit {
		events = events[:limit]
	}

	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// VerifyChain walks the tamper-evident hash chain and returns its integrity status.
func (h *AuditHandler) VerifyChain(w http.ResponseWriter, r *http.Request) {
	result, err := audit.Verify(r.Context(), h.queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "verification failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
