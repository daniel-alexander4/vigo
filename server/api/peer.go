// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/hobnob/hobnob/server/peer"
)

// PeerHandler serves the /api/v1/peer/ endpoints for peer replication status
// and promotion.
type PeerHandler struct {
	peerSvc *peer.Service
}

// NewPeerHandler creates a handler for peer replication REST endpoints.
// If peerSvc is nil, the endpoints return 404 (peer replication not configured).
func NewPeerHandler(peerSvc *peer.Service) *PeerHandler {
	return &PeerHandler{peerSvc: peerSvc}
}

// RegisterRoutes mounts the peer endpoints on the given mux.
func (h *PeerHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/peer/status", h.handleStatus)
	mux.HandleFunc("POST /api/v1/peer/promote", h.handlePromote)
}

// peerStatusResponse is the JSON shape returned by GET /api/v1/peer/status.
type peerStatusResponse struct {
	Role          string `json:"role"`
	PrimaryAddr   string `json:"primary_addr,omitempty"`
	LastSyncTime  string `json:"last_sync_time,omitempty"`
	FilesSynced   int64  `json:"files_synced"`
	ConfigVersion uint64 `json:"config_version"`
}

func (h *PeerHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if h.peerSvc == nil {
		http.Error(w, `{"error":"peer replication not configured"}`, http.StatusNotFound)
		return
	}

	resp, err := h.peerSvc.Status(r.Context(), nil)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}

	out := peerStatusResponse{
		Role:          resp.Role,
		PrimaryAddr:   resp.PrimaryAddr,
		FilesSynced:   resp.FilesSynced,
		ConfigVersion: resp.ConfigVersion,
	}
	if resp.LastSyncUnix > 0 {
		out.LastSyncTime = time.Unix(resp.LastSyncUnix, 0).UTC().Format(time.RFC3339)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// peerPromoteResponse is the JSON shape returned by POST /api/v1/peer/promote.
type peerPromoteResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func (h *PeerHandler) handlePromote(w http.ResponseWriter, r *http.Request) {
	if h.peerSvc == nil {
		http.Error(w, `{"error":"peer replication not configured"}`, http.StatusNotFound)
		return
	}

	resp, err := h.peerSvc.Promote(r.Context(), nil)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(peerPromoteResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(peerPromoteResponse{
		Success: resp.Success,
		Message: resp.Message,
	})
}
