// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/tungol"
)

// TungolStatus is the response for GET /api/v1/tungol/status.
//
// Phase 2A (ADR-026): the operator surface uses `mode`
// (founder|joiner|standalone) instead of the retired `role` field.
// TungolID and LocalSteorraPubkey surface this vigosrv's federation
// anchor + identity for operator cross-check when an alarm fires.
type TungolStatus struct {
	Mode               string                 `json:"mode"`
	TungolID           string                 `json:"tungol_id,omitempty"`
	LocalSteorraPubkey string                 `json:"local_steorra_pubkey,omitempty"`
	Steorras           []tungol.SteorraStatus `json:"steorras,omitempty"`
	SteorraPins        []SteorraPinView       `json:"steorra_pins,omitempty"`
}

// SteorraPinView is the operator-visible projection of a
// Hub.SteorraPin record. Hex-encoded pubkey + RFC3339 timestamps
// so vigocli renders them without further conversion.
type SteorraPinView struct {
	TungolID  string `json:"tungol_id"`
	SteorraID string `json:"steorra_id"`
	Pubkey    string `json:"pubkey"`
	PinnedAt  string `json:"pinned_at"`
	LastSeen  string `json:"last_seen"`
}

// TungolHandler serves tungol status endpoints. Phase 2A keeps the
// hub-as-bootstrap-rendezvous shape for admin-routing under ADR-026;
// Phase 3 retires the hub entirely.
type TungolHandler struct {
	mode        config.TungolMode
	tungolID    string
	localPubkey string // first-16-char hex of the local steorra identity (or "" if absent)
	hub         *tungol.Hub
}

// NewTungolHandler creates a tungol handler.
func NewTungolHandler(mode config.TungolMode, tungolID, localPubkey string, hub *tungol.Hub) *TungolHandler {
	return &TungolHandler{
		mode:        mode,
		tungolID:    tungolID,
		localPubkey: localPubkey,
		hub:         hub,
	}
}

// RegisterRoutes mounts tungol endpoints on the given mux. Mutations
// (push / reassign / drain) are admin-only — they flip envoy-steorra
// assignments and force fan-out. Read endpoints (status + steorra
// drill-downs) gate on fleet.read; they expose tungol-aggregated
// fleet state.
func (h *TungolHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	admin := auth.RequireAdmin("")
	mux.Handle("GET /api/v1/tungol/status", read(http.HandlerFunc(h.handleStatus)))
	if h.hub != nil {
		mux.Handle("GET /api/v1/tungol/steorras/{id}", read(http.HandlerFunc(h.handleSteorraDetail)))
		mux.Handle("GET /api/v1/tungol/steorras/{id}/envoys", read(http.HandlerFunc(h.handleSteorraEnvoys)))
		mux.Handle("GET /api/v1/tungol/steorras/{id}/runs", read(http.HandlerFunc(h.handleSteorraRuns)))
		mux.Handle("POST /api/v1/tungol/steorras/{id}/push", admin(http.HandlerFunc(h.handleSteorraPush)))
		mux.Handle("POST /api/v1/tungol/reassign", admin(http.HandlerFunc(h.handleReassign)))
		mux.Handle("POST /api/v1/tungol/steorras/{id}/drain", admin(http.HandlerFunc(h.handleDrain)))
	}
}

func (h *TungolHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	mode := string(h.mode)
	if mode == "" {
		mode = "standalone"
	}

	resp := TungolStatus{
		Mode:               mode,
		TungolID:           h.tungolID,
		LocalSteorraPubkey: h.localPubkey,
	}

	if h.hub != nil {
		resp.Steorras = h.hub.SteorraStatuses(r.Context())
		// Surface pinned identities so operators can cross-check the
		// hub's view against the local steorra pubkey on each spoke.
		for _, s := range resp.Steorras {
			pin := h.hub.LookupSteorraPin(h.tungolID, s.ID)
			if pin == nil {
				continue
			}
			resp.SteorraPins = append(resp.SteorraPins, SteorraPinView{
				TungolID:  pin.TungolID,
				SteorraID: pin.SteorraID,
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
// Operators cross-reference this against the LocalSteorraPubkey
// from the steorra's own /api/v1/tungol/status.
func formatHexShort(pk []byte) string {
	const wantHex = 16
	const wantBytes = wantHex / 2
	if len(pk) < wantBytes {
		return hex.EncodeToString(pk)
	}
	return hex.EncodeToString(pk[:wantBytes])
}

func (h *TungolHandler) handleSteorraDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	statuses := h.hub.SteorraStatuses(r.Context())
	for _, s := range statuses {
		if s.ID == id {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(s)
			return
		}
	}
	writeError(w, http.StatusNotFound, "steorra not found")
}

func (h *TungolHandler) handleSteorraEnvoys(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	envoys, err := h.hub.SteorraListEnvoys(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"envoys": envoys})
}

func (h *TungolHandler) handleSteorraRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := int32(50)
	runs, err := h.hub.SteorraListRuns(r.Context(), id, limit)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"runs": runs})
}

func (h *TungolHandler) handleSteorraPush(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		EnvoyID string `json:"envoy_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	count, err := h.hub.SteorraForcePush(r.Context(), id, req.EnvoyID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"envoys_notified": count})
}

func (h *TungolHandler) handleReassign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EnvoyID      string `json:"envoy_id"`
		FromSteorraID string `json:"from_steorra_id"`
		ToSteorraID   string `json:"to_steorra_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.EnvoyID == "" || req.FromSteorraID == "" || req.ToSteorraID == "" {
		writeError(w, http.StatusBadRequest, "envoy_id, from_steorra_id, and to_steorra_id are required")
		return
	}

	result, err := h.hub.ReassignEnvoy(r.Context(), req.EnvoyID, req.FromSteorraID, req.ToSteorraID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *TungolHandler) handleDrain(w http.ResponseWriter, r *http.Request) {
	steorraID := r.PathValue("id")
	var req struct {
		TargetSteorraID string `json:"target_steorra_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	result, err := h.hub.DrainSteorra(r.Context(), steorraID, req.TargetSteorraID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
