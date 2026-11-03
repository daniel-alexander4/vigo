// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/peer"
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

// RegisterRoutes mounts the peer endpoints on the given mux. Status
// is fleet-read; promote (which flips a secondary peer to primary and
// reshapes replication) is admin-only.
func (h *PeerHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	admin := auth.RequireAdmin("")
	mux.Handle("GET /api/v1/peer/status", read(http.HandlerFunc(h.handleStatus)))
	mux.Handle("POST /api/v1/peer/promote", admin(http.HandlerFunc(h.handlePromote)))
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
		writeError(w, http.StatusNotFound, "peer replication not configured")
		return
	}

	resp, err := h.peerSvc.Status(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
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

	writeJSON(w, r, http.StatusOK, out)
}

// peerPromoteResponse is the JSON shape returned by POST /api/v1/peer/promote.
type peerPromoteResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func (h *PeerHandler) handlePromote(w http.ResponseWriter, r *http.Request) {
	if h.peerSvc == nil {
		writeError(w, http.StatusNotFound, "peer replication not configured")
		return
	}

	resp, err := h.peerSvc.Promote(r.Context(), nil)
	if err != nil {
		writeJSON(w, r, http.StatusInternalServerError, peerPromoteResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	writeJSON(w, r, http.StatusOK, peerPromoteResponse{
		Success: resp.Success,
		Message: resp.Message,
	})
}
