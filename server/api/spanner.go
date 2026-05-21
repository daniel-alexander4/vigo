// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/spanner"
)

// SpannerStatus is the response for GET /api/v1/spanner/status.
//
// Phase 2A (ADR-026): the operator surface uses `mode`
// (founder|joiner|standalone) instead of the retired `role` field.
// SpannerID and LocalBoltPubkey surface this vigosrv's federation
// anchor + identity for operator cross-check when an alarm fires.
type SpannerStatus struct {
	Mode            string               `json:"mode"`
	SpannerID       string               `json:"spanner_id,omitempty"`
	LocalBoltPubkey string               `json:"local_bolt_pubkey,omitempty"`
	Bolts           []spanner.BoltStatus `json:"bolts,omitempty"`
	BoltPins        []BoltPinView        `json:"bolt_pins,omitempty"`
}

// BoltPinView is the operator-visible projection of a
// Hub.BoltPin record. Hex-encoded pubkey + RFC3339 timestamps
// so vigocli renders them without further conversion.
type BoltPinView struct {
	SpannerID string `json:"spanner_id"`
	BoltID    string `json:"bolt_id"`
	Pubkey    string `json:"pubkey"`
	PinnedAt  string `json:"pinned_at"`
	LastSeen  string `json:"last_seen"`
}

// SpannerHandler serves spanner status endpoints. Phase 2A keeps the
// hub-as-bootstrap-rendezvous shape for admin-routing under ADR-026;
// Phase 3 retires the hub entirely.
type SpannerHandler struct {
	mode        config.SpannerMode
	spannerID   string
	localPubkey string // first-16-char hex of the local bolt identity (or "" if absent)
	hub         *spanner.Hub
}

// NewSpannerHandler creates a spanner handler.
func NewSpannerHandler(mode config.SpannerMode, spannerID, localPubkey string, hub *spanner.Hub) *SpannerHandler {
	return &SpannerHandler{
		mode:        mode,
		spannerID:   spannerID,
		localPubkey: localPubkey,
		hub:         hub,
	}
}

// RegisterRoutes mounts spanner endpoints on the given mux. Mutations
// (push / reassign / drain) are admin-only — they flip envoy-bolt
// assignments and force fan-out. Read endpoints (status + bolt
// drill-downs) gate on fleet.read; they expose spanner-aggregated
// fleet state.
func (h *SpannerHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	admin := auth.RequireAdmin("")
	mux.Handle("GET /api/v1/spanner/status", read(http.HandlerFunc(h.handleStatus)))
	if h.hub != nil {
		mux.Handle("GET /api/v1/spanner/bolts/{id}", read(http.HandlerFunc(h.handleBoltDetail)))
		mux.Handle("GET /api/v1/spanner/bolts/{id}/envoys", read(http.HandlerFunc(h.handleBoltEnvoys)))
		mux.Handle("GET /api/v1/spanner/bolts/{id}/runs", read(http.HandlerFunc(h.handleBoltRuns)))
		mux.Handle("POST /api/v1/spanner/bolts/{id}/push", admin(http.HandlerFunc(h.handleBoltPush)))
		mux.Handle("POST /api/v1/spanner/reassign", admin(http.HandlerFunc(h.handleReassign)))
		mux.Handle("POST /api/v1/spanner/bolts/{id}/drain", admin(http.HandlerFunc(h.handleDrain)))
	}
}

func (h *SpannerHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	mode := string(h.mode)
	if mode == "" {
		mode = "standalone"
	}

	resp := SpannerStatus{
		Mode:            mode,
		SpannerID:       h.spannerID,
		LocalBoltPubkey: h.localPubkey,
	}

	if h.hub != nil {
		resp.Bolts = h.hub.BoltStatuses(r.Context())
		// Surface pinned identities so operators can cross-check the
		// hub's view against the local bolt pubkey on each spoke.
		for _, s := range resp.Bolts {
			pin := h.hub.LookupBoltPin(h.spannerID, s.ID)
			if pin == nil {
				continue
			}
			resp.BoltPins = append(resp.BoltPins, BoltPinView{
				SpannerID: pin.SpannerID,
				BoltID:    pin.BoltID,
				Pubkey:    formatHexShort(pin.Pubkey),
				PinnedAt:  pin.PinnedAt.UTC().Format(time.RFC3339),
				LastSeen:  pin.LastSeen.UTC().Format(time.RFC3339),
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// formatHexShort returns first-16-char lowercase hex of pk.
// Operators cross-reference this against the LocalBoltPubkey
// from the bolt's own /api/v1/spanner/status.
func formatHexShort(pk []byte) string {
	const wantHex = 16
	const wantBytes = wantHex / 2
	if len(pk) < wantBytes {
		return hex.EncodeToString(pk)
	}
	return hex.EncodeToString(pk[:wantBytes])
}

func (h *SpannerHandler) handleBoltDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	statuses := h.hub.BoltStatuses(r.Context())
	for _, s := range statuses {
		if s.ID == id {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(s)
			return
		}
	}
	writeError(w, http.StatusNotFound, "bolt not found")
}

func (h *SpannerHandler) handleBoltEnvoys(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	envoys, err := h.hub.BoltListEnvoys(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"envoys": envoys})
}

func (h *SpannerHandler) handleBoltRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := int32(50)
	runs, err := h.hub.BoltListRuns(r.Context(), id, limit)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"runs": runs})
}

func (h *SpannerHandler) handleBoltPush(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		EnvoyID string `json:"envoy_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	count, err := h.hub.BoltForcePush(r.Context(), id, req.EnvoyID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"envoys_notified": count})
}

func (h *SpannerHandler) handleReassign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EnvoyID    string `json:"envoy_id"`
		FromBoltID string `json:"from_bolt_id"`
		ToBoltID   string `json:"to_bolt_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.EnvoyID == "" || req.FromBoltID == "" || req.ToBoltID == "" {
		writeError(w, http.StatusBadRequest, "envoy_id, from_bolt_id, and to_bolt_id are required")
		return
	}

	result, err := h.hub.ReassignEnvoy(r.Context(), req.EnvoyID, req.FromBoltID, req.ToBoltID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *SpannerHandler) handleDrain(w http.ResponseWriter, r *http.Request) {
	boltID := r.PathValue("id")
	var req struct {
		TargetBoltID string `json:"target_bolt_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	result, err := h.hub.DrainBolt(r.Context(), boltID, req.TargetBoltID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
