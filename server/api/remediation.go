// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	vigoauth "github.com/daniel-alexander4/vigo/server/auth"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
)

// RemediationHandler manages automated drift detection → fix → verify cycles.
type RemediationHandler struct {
	queries     *db.Queries
	fleetIndex  *fleet.FleetIndex
	auditWriter auditRecorder

	mu   sync.Mutex
	runs map[string]*RemediationRun
}

// RemediationRun tracks a remediation cycle.
type RemediationRun struct {
	ID          string             `json:"id"`
	Status      string             `json:"status"` // "running", "complete", "failed"
	Operator    string             `json:"operator"`
	StartedAt   time.Time          `json:"started_at"`
	FinishedAt  *time.Time         `json:"finished_at,omitempty"`
	Targets     []RemediationTarget `json:"targets"`
	Summary     string             `json:"summary,omitempty"`
}

// RemediationTarget is a single node in a remediation cycle.
type RemediationTarget struct {
	Hostname       string `json:"hostname"`
	EnvoyID       string `json:"envoy_id"`
	InitialStatus  string `json:"initial_status"`
	Action         string `json:"action"`  // "force_push", "skipped"
	FinalStatus    string `json:"final_status,omitempty"`
}

// NewRemediationHandler creates a RemediationHandler.
func NewRemediationHandler(queries *db.Queries, fleetIndex *fleet.FleetIndex) *RemediationHandler {
	return &RemediationHandler{
		queries:    queries,
		fleetIndex: fleetIndex,
		runs:       make(map[string]*RemediationRun),
	}
}

// SetAuditWriter sets the audit writer.
func (h *RemediationHandler) SetAuditWriter(w auditRecorder) { h.auditWriter = w }

// RegisterRoutes mounts remediation endpoints.
func (h *RemediationHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/remediation/run", h.handleRun)
	mux.HandleFunc("GET /api/v1/remediation/runs", h.handleList)
	mux.HandleFunc("GET /api/v1/remediation/runs/{id}", h.handleGet)
	mux.HandleFunc("GET /api/v1/remediation/status", h.handleStatus)
}

type remediationRunRequest struct {
	DryRun bool `json:"dry_run"`
}

func (h *RemediationHandler) handleRun(w http.ResponseWriter, r *http.Request) {
	var req remediationRunRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}

	operator := "system"
	if u := vigoauth.UserFromContext(r.Context()); u != nil {
		operator = u.Username
	}

	// Find non-compliant nodes.
	allEnvoys := h.fleetIndex.AllEnvoys()
	var targets []RemediationTarget
	for _, t := range allEnvoys {
		if t.Revoked {
			continue
		}
		switch t.ConvergenceStatus {
		case "relapsed", "diverged", "failed":
			action := "force_push"
			if req.DryRun {
				action = "would_force_push"
			}
			targets = append(targets, RemediationTarget{
				Hostname:      t.Hostname,
				EnvoyID:      t.ID,
				InitialStatus: t.ConvergenceStatus,
				Action:        action,
			})
		}
	}

	if len(targets) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "no_action",
			"message": "all nodes are compliant — no remediation needed",
		})
		return
	}

	if req.DryRun {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "dry_run",
			"targets": targets,
			"count":   len(targets),
		})
		return
	}

	// Create a run and dispatch force-pushes.
	run := &RemediationRun{
		ID:        fmt.Sprintf("rem-%d", time.Now().UnixNano()),
		Status:    "running",
		Operator:  operator,
		StartedAt: time.Now().UTC(),
		Targets:   targets,
	}

	h.mu.Lock()
	h.runs[run.ID] = run
	h.mu.Unlock()

	// Dispatch force-pushes asynchronously.
	go h.executeRemediation(run)

	recordAudit(r.Context(), h.auditWriter, "remediation.start", operator, "rest",
		fmt.Sprintf("remediation started for %d nodes", len(targets)), "")

	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":      run.ID,
		"status":  "running",
		"targets": len(targets),
	})
}

func (h *RemediationHandler) executeRemediation(run *RemediationRun) {
	ctx := context.Background()

	for i := range run.Targets {
		t := &run.Targets[i]
		// Trigger a force-push via FleetIndex (marks node for immediate check-in).
		if h.fleetIndex != nil {
			h.fleetIndex.MarkDirty(t.EnvoyID)
		}
		t.Action = "force_push"
	}

	// Wait for nodes to reconverge (up to 5 minutes).
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(15 * time.Second)

		allResolved := true
		for i := range run.Targets {
			t := &run.Targets[i]
			if t.FinalStatus != "" {
				continue
			}
			envoy := h.fleetIndex.GetEnvoy(t.EnvoyID)
			if envoy == nil {
				t.FinalStatus = "unknown"
				continue
			}
			if envoy.ConvergenceStatus == "converged" {
				t.FinalStatus = "converged"
			} else if envoy.LastSeen.After(run.StartedAt) {
				// Node checked in since remediation started but still non-compliant.
				t.FinalStatus = envoy.ConvergenceStatus
			} else {
				allResolved = false
			}
		}
		if allResolved {
			break
		}
	}

	// Fill in any unresolved targets.
	remediated := 0
	for i := range run.Targets {
		t := &run.Targets[i]
		if t.FinalStatus == "" {
			t.FinalStatus = "timeout"
		}
		if t.FinalStatus == "converged" {
			remediated++
		}
	}

	now := time.Now().UTC()
	run.FinishedAt = &now
	run.Status = "complete"
	run.Summary = fmt.Sprintf("%d/%d nodes remediated", remediated, len(run.Targets))

	if remediated < len(run.Targets) {
		run.Status = "partial"
	}

	recordAudit(ctx, h.auditWriter, "remediation.complete", run.Operator, "rest", run.Summary, "")
	slog.Info("remediation complete", "id", run.ID, "summary", run.Summary)
}

func (h *RemediationHandler) handleList(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	runs := make([]*RemediationRun, 0, len(h.runs))
	for _, run := range h.runs {
		runs = append(runs, run)
	}
	h.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (h *RemediationHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	h.mu.Lock()
	run, ok := h.runs[id]
	h.mu.Unlock()

	if !ok {
		writeJSONError(w, http.StatusNotFound, "remediation run not found")
		return
	}

	writeJSON(w, http.StatusOK, run)
}

// handleStatus returns the current fleet remediation posture — how many nodes
// need remediation and a breakdown by status.
func (h *RemediationHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	summary := h.fleetIndex.ConvergenceSummary()
	needsRemediation := summary.Relapsed + summary.Diverged + summary.Failed

	writeJSON(w, http.StatusOK, map[string]any{
		"needs_remediation": needsRemediation,
		"relapsed":          summary.Relapsed,
		"diverged":          summary.Diverged,
		"failed":            summary.Failed,
		"total":             summary.Total,
		"converged":         summary.Converged,
	})
}
