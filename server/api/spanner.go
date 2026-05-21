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
	// PeerObservability surfaces the Slice 2-ii-a gossip-backed
	// per-peer fleet facts in peer-equal mode (no hub). Empty when
	// running with a hub or in standalone mode.
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
// Phase 3 retires the hub entirely. In peer-equal mode (no hub) the
// bolt drill-downs are served from the Slice 2-ii-a observability
// cache via spannerAdmin.
type SpannerHandler struct {
	mode        config.SpannerMode
	spannerID   string
	localPubkey string // first-16-char hex of the local bolt identity (or "" if absent)
	hub         *spanner.Hub
	// spannerAdmin drives the peer-equal observability surfaces (Slice
	// 2-ii-a). nil in standalone mode or before it is wired.
	spannerAdmin *spanner.SpannerAdmin
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

// SetSpannerAdmin attaches the peer-equal admin-read driver (Slice
// 2-ii-a) so the status + bolt-drilldown surfaces work in peer-equal
// mode where there is no hub.
func (h *SpannerHandler) SetSpannerAdmin(a *spanner.SpannerAdmin) {
	h.spannerAdmin = a
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
	} else {
		// Peer-equal mode: the bolt drill-downs are served from the
		// gossip-backed observability cache rather than a hub fan-out
		// (Slice 2-ii-a). The mutating push/reassign/drain verbs route
		// point-to-point over the gossiped roster via SpannerAdmin
		// (Slice 2-ii-b) — no hub, no gossiped intent channel.
		mux.Handle("GET /api/v1/spanner/bolts/{id}/envoys", read(http.HandlerFunc(h.handlePeerBoltEnvoys)))
		mux.Handle("POST /api/v1/spanner/bolts/{id}/push", admin(http.HandlerFunc(h.handlePeerBoltPush)))
		mux.Handle("POST /api/v1/spanner/reassign", admin(http.HandlerFunc(h.handlePeerReassign)))
		mux.Handle("POST /api/v1/spanner/bolts/{id}/drain", admin(http.HandlerFunc(h.handlePeerDrain)))
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
	} else if h.spannerAdmin != nil {
		// Peer-equal mode (Slice 2-ii-a): surface the gossip-backed
		// per-peer observability instead of a hub fan-out.
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
			if !p.UpdatedAt.IsZero() {
				view.SnapshotAgeSeconds = int64(now.Sub(p.UpdatedAt).Seconds())
			}
			resp.PeerObservability = append(resp.PeerObservability, view)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"envoys": p.Envoys})
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"envoys_notified": count})
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
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
