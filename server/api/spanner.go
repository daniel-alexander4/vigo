// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/spanner"
)

// SpannerStatus is the response for GET /api/v1/spanner/status.
//
// ADR-026 peer-equal model: the operator surface uses `mode`
// (standalone|spanner) instead of the retired `role` field. SpannerID
// and LocalBoltPubkey surface this vigosrv's federation anchor +
// identity for operator cross-check when an alarm fires.
type SpannerStatus struct {
	Mode            string `json:"mode"`
	SpannerID       string `json:"spanner_id,omitempty"`
	LocalBoltPubkey string `json:"local_bolt_pubkey,omitempty"`
	// PeerObservability surfaces the gossip-backed per-peer fleet
	// facts. Empty in standalone mode.
	PeerObservability []SpannerPeerView `json:"peer_observability,omitempty"`
}

// SpannerPeerView is the operator-visible projection of one peer
// bolt's cached observability entry — the envoy count, convergence
// breakdown, and how stale the data is.
type SpannerPeerView struct {
	BoltPubkey         string `json:"bolt_pubkey"`
	EnvoyCount         int    `json:"envoy_count"`
	Converged          int    `json:"converged"`
	Degraded           int    `json:"degraded"`
	Failed             int    `json:"failed"`
	NoData             int    `json:"no_data"`
	Offline            int    `json:"offline"`
	SnapshotAgeSeconds int64  `json:"snapshot_age_seconds"`
}

// SpannerHandler serves spanner status endpoints. The bolt
// drill-downs are served from the gossip-backed observability cache
// (Slice 2-ii-a) and the action verbs route point-to-point over the
// gossiped roster (Slice 2-ii-b) via spannerAdmin.
type SpannerHandler struct {
	mode        config.SpannerMode
	spannerID   string
	localPubkey string // first-16-char hex of the local bolt identity (or "" if absent)
	// spannerAdmin drives the peer-equal observability + action
	// surfaces. nil in standalone mode or before it is wired.
	spannerAdmin *spanner.SpannerAdmin
	auditWriter  auditRecorder
}

// NewSpannerHandler creates a spanner handler.
func NewSpannerHandler(mode config.SpannerMode, spannerID, localPubkey string) *SpannerHandler {
	return &SpannerHandler{
		mode:        mode,
		spannerID:   spannerID,
		localPubkey: localPubkey,
	}
}

// SetSpannerAdmin attaches the peer-equal admin driver so the status,
// bolt-drilldown, and action surfaces work in spanner mode.
func (h *SpannerHandler) SetSpannerAdmin(a *spanner.SpannerAdmin) {
	h.spannerAdmin = a
}

// SetAuditWriter attaches the tamper-evident audit writer.
func (h *SpannerHandler) SetAuditWriter(w auditRecorder) { h.auditWriter = w }

// RegisterRoutes mounts spanner endpoints on the given mux. Mutations
// (push / reassign / drain) are admin-only — they flip envoy-bolt
// assignments and force fan-out. Read endpoints (status + bolt
// drill-downs) gate on fleet.read; they expose spanner-aggregated
// fleet state.
func (h *SpannerHandler) RegisterRoutes(mux *http.ServeMux) {
	read := auth.RequirePermission(auth.PermFleetRead)
	admin := auth.RequireAdmin()
	mux.Handle("GET /api/v1/spanner/status", read(http.HandlerFunc(h.handleStatus)))
	// Bolt drill-downs are served from the gossip-backed observability
	// cache (Slice 2-ii-a). The mutating push/reassign/drain verbs route
	// point-to-point over the gossiped roster via SpannerAdmin (Slice
	// 2-ii-b). All guard on a nil spannerAdmin (standalone mode).
	mux.Handle("GET /api/v1/spanner/bolts/{id}", read(http.HandlerFunc(h.handlePeerBoltDetail)))
	mux.Handle("GET /api/v1/spanner/bolts/{id}/envoys", read(http.HandlerFunc(h.handlePeerBoltEnvoys)))
	mux.Handle("GET /api/v1/spanner/bolts/{id}/runs", read(http.HandlerFunc(h.handlePeerBoltRuns)))
	mux.Handle("POST /api/v1/spanner/bolts/{id}/push", admin(http.HandlerFunc(h.handlePeerBoltPush)))
	mux.Handle("POST /api/v1/spanner/reassign", admin(http.HandlerFunc(h.handlePeerReassign)))
	mux.Handle("POST /api/v1/spanner/bolts/{id}/drain", admin(http.HandlerFunc(h.handlePeerDrain)))
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

	if h.spannerAdmin != nil {
		// Surface the gossip-backed per-peer observability.
		now := time.Now()
		for pubkey, p := range h.spannerAdmin.AllPeerObservability() {
			view := SpannerPeerView{
				BoltPubkey: pubkey,
				EnvoyCount: len(p.Envoys),
				Converged:  p.Convergence.Converged,
				Degraded:   p.Convergence.Degraded,
				Failed:     p.Convergence.Failed,
				NoData:     p.Convergence.NoData,
				Offline:    p.Convergence.Offline,
			}
			if u := p.UpdatedAt(); !u.IsZero() {
				view.SnapshotAgeSeconds = int64(now.Sub(u).Seconds())
			}
			resp.PeerObservability = append(resp.PeerObservability, view)
		}
	}

	writeJSON(w, r, http.StatusOK, resp)
}

// handlePeerBoltEnvoys serves one peer bolt's cached envoy roster in
// peer-equal mode — the gossip-backed analog of the hub-fan-out
// handleBoltEnvoys. The {id} path value is the peer bolt's pubkey-hex.
func (h *SpannerHandler) handlePeerBoltEnvoys(w http.ResponseWriter, r *http.Request) {
	if h.spannerAdmin == nil {
		writeError(w, http.StatusServiceUnavailable, "spanner observability unavailable")
		return
	}
	id := r.PathValue("id")
	p := h.spannerAdmin.PeerObservability(id)
	if p == nil {
		writeError(w, http.StatusNotFound, "no cached observability for bolt "+id)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"envoys": p.Envoys})
}

// handlePeerBoltPush is the peer-equal analog of handleBoltPush: it
// routes a ForcePush to one bolt's envoys via a direct admin RPC over
// the gossiped roster (Slice 2-ii-b). The {id} path value is the
// target bolt's pubkey-hex; an empty body envoy_id means "every envoy
// on that bolt".
func (h *SpannerHandler) handlePeerBoltPush(w http.ResponseWriter, r *http.Request) {
	if h.spannerAdmin == nil {
		writeError(w, http.StatusServiceUnavailable, "spanner action propagation unavailable")
		return
	}
	id := r.PathValue("id")
	var req struct {
		EnvoyID string `json:"envoy_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	hostname := h.spannerAdmin.BoltHostname(r.Context(), id)
	if hostname == "" {
		writeError(w, http.StatusNotFound, "no roster bolt for "+id)
		return
	}
	count, err := h.spannerAdmin.ForcePushToBolt(r.Context(), hostname, req.EnvoyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordAudit(r.Context(), h.auditWriter, "spanner.bolt_push", actorFromRequest(r), "rest",
		fmt.Sprintf("force-pushed bolt %s envoy=%q (%d envoys notified)", id, req.EnvoyID, count), "")
	writeJSON(w, r, http.StatusOK, map[string]any{"envoys_notified": count})
}

// handlePeerReassign is the peer-equal analog of handleReassign: it
// orchestrates an envoy move with three direct peer admin RPCs (Slice
// 2-ii-b) instead of going through a hub. from_bolt_id / to_bolt_id
// are bolt pubkey-hex; an empty to_bolt_id auto-routes the envoy by
// hostname through OwningBolt.
func (h *SpannerHandler) handlePeerReassign(w http.ResponseWriter, r *http.Request) {
	if h.spannerAdmin == nil {
		writeError(w, http.StatusServiceUnavailable, "spanner action propagation unavailable")
		return
	}
	var req struct {
		EnvoyID    string `json:"envoy_id"`
		FromBoltID string `json:"from_bolt_id"`
		ToBoltID   string `json:"to_bolt_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.EnvoyID == "" || req.FromBoltID == "" {
		writeError(w, http.StatusBadRequest, "envoy_id and from_bolt_id are required")
		return
	}
	fromHostname := h.spannerAdmin.BoltHostname(r.Context(), req.FromBoltID)
	if fromHostname == "" {
		writeError(w, http.StatusNotFound, "no roster bolt for source "+req.FromBoltID)
		return
	}
	toHostname := ""
	if req.ToBoltID != "" {
		toHostname = h.spannerAdmin.BoltHostname(r.Context(), req.ToBoltID)
		if toHostname == "" {
			writeError(w, http.StatusNotFound, "no roster bolt for target "+req.ToBoltID)
			return
		}
	}
	result, err := h.spannerAdmin.ReassignEnvoy(r.Context(), req.EnvoyID, fromHostname, toHostname)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordAudit(r.Context(), h.auditWriter, "spanner.peer_reassign", actorFromRequest(r), "rest",
		fmt.Sprintf("reassigned envoy %s from bolt %s to bolt %s", req.EnvoyID, req.FromBoltID, req.ToBoltID), "")
	writeJSON(w, r, http.StatusOK, result)
}

// handlePeerDrain is the peer-equal analog of handleDrain: it moves
// every envoy off the {id} bolt onto other bolts via batched
// peer-equal reassignment (Slice 2-ii-b). The {id} path value and the
// optional target_bolt_id body field are bolt pubkey-hex; an empty
// target auto-routes each envoy by hostname.
func (h *SpannerHandler) handlePeerDrain(w http.ResponseWriter, r *http.Request) {
	if h.spannerAdmin == nil {
		writeError(w, http.StatusServiceUnavailable, "spanner action propagation unavailable")
		return
	}
	boltID := r.PathValue("id")
	var req struct {
		TargetBoltID string `json:"target_bolt_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	fromHostname := h.spannerAdmin.BoltHostname(r.Context(), boltID)
	if fromHostname == "" {
		writeError(w, http.StatusNotFound, "no roster bolt for "+boltID)
		return
	}
	toHostname := ""
	if req.TargetBoltID != "" {
		toHostname = h.spannerAdmin.BoltHostname(r.Context(), req.TargetBoltID)
		if toHostname == "" {
			writeError(w, http.StatusNotFound, "no roster bolt for target "+req.TargetBoltID)
			return
		}
	}
	result, err := h.spannerAdmin.DrainBolt(r.Context(), fromHostname, toHostname)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recordAudit(r.Context(), h.auditWriter, "spanner.bolt_drain", actorFromRequest(r), "rest",
		fmt.Sprintf("drained bolt %s onto %s", boltID, req.TargetBoltID), "")
	writeJSON(w, r, http.StatusOK, result)
}

// handlePeerBoltDetail serves one peer bolt's cached observability
// summary — the gossip-backed analog of the retired hub-fan-out bolt
// detail. The {id} path value is the peer bolt's pubkey-hex.
func (h *SpannerHandler) handlePeerBoltDetail(w http.ResponseWriter, r *http.Request) {
	if h.spannerAdmin == nil {
		writeError(w, http.StatusServiceUnavailable, "spanner observability unavailable")
		return
	}
	id := r.PathValue("id")
	p := h.spannerAdmin.PeerObservability(id)
	if p == nil {
		writeError(w, http.StatusNotFound, "no cached observability for bolt "+id)
		return
	}
	view := SpannerPeerView{
		BoltPubkey: id,
		EnvoyCount: len(p.Envoys),
		Converged:  p.Convergence.Converged,
		Degraded:   p.Convergence.Degraded,
		Failed:     p.Convergence.Failed,
		NoData:     p.Convergence.NoData,
		Offline:    p.Convergence.Offline,
	}
	if u := p.UpdatedAt(); !u.IsZero() {
		view.SnapshotAgeSeconds = int64(time.Since(u).Seconds())
	}
	writeJSON(w, r, http.StatusOK, view)
}

// handlePeerBoltRuns serves one peer bolt's runs via a direct admin
// RPC over the gossiped roster (SpannerAdmin.RunsFromBolt). Runs are
// never gossiped, so unlike the envoy drill-down this is a live fetch
// rather than a cache read. The {id} path value is the bolt's
// pubkey-hex.
func (h *SpannerHandler) handlePeerBoltRuns(w http.ResponseWriter, r *http.Request) {
	if h.spannerAdmin == nil {
		writeError(w, http.StatusServiceUnavailable, "spanner observability unavailable")
		return
	}
	id := r.PathValue("id")
	hostname := h.spannerAdmin.BoltHostname(r.Context(), id)
	if hostname == "" {
		writeError(w, http.StatusNotFound, "no roster bolt for "+id)
		return
	}
	runs, err := h.spannerAdmin.RunsFromBolt(r.Context(), hostname, "", 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"runs": runs})
}
