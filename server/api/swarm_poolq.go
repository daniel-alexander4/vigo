// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/swarm/poolqmesh"
)

// PoolqHandler serves the poolq (ADR-029) REST surface — the read path
// every consumer (agent + vigocli + admin UI) goes through. Aggregation
// lives in `poolqmesh`; consumers verify signed messages end-to-end, so
// the server is purely a relay (it doesn't validate signatures — see the
// poolqmesh package doc).
//
// All read endpoints are public-by-construction — poolq messages are
// fleet-readable (ADR-029 explicitly: confidentiality is not a goal; pair
// with lockbox for confidential payloads), so there's nothing to
// authenticate beyond the TLS transport, exactly like
// `/api/v1/swarm/curator/resolve`. The `block`/`unblock` admin moderation
// endpoints land in Slice 5.
type PoolqHandler struct {
	agg        *poolqmesh.Aggregator
	queries    *db.Queries
	fleetIndex *fleet.FleetIndex
}

// NewPoolqHandler builds the handler.
func NewPoolqHandler(agg *poolqmesh.Aggregator, queries *db.Queries, fleetIndex *fleet.FleetIndex) *PoolqHandler {
	return &PoolqHandler{agg: agg, queries: queries, fleetIndex: fleetIndex}
}

// RegisterRoutes mounts the poolq endpoints. Read endpoints are public
// (matching public-path entries live in `server/auth/auth.go`); the
// admin moderation endpoints are admin-only.
func (h *PoolqHandler) RegisterRoutes(mux *http.ServeMux) {
	admin := auth.RequireAdmin("")
	mux.HandleFunc("GET /api/v1/swarm/poolq/topics", h.handleTopics)
	mux.HandleFunc("GET /api/v1/swarm/poolq/topic/{topic_id}/head", h.handleHead)
	mux.HandleFunc("GET /api/v1/swarm/poolq/topic/{topic_id}/range", h.handleRange)
	mux.HandleFunc("GET /api/v1/swarm/poolq/topic/{topic_id}/log", h.handleLog)
	mux.Handle("POST /api/v1/swarm/poolq/block/{topic_id}", admin(http.HandlerFunc(h.handleBlock)))
	mux.Handle("POST /api/v1/swarm/poolq/unblock/{topic_id}", admin(http.HandlerFunc(h.handleUnblock)))
}

// blockedSet returns the set of currently-blocked topic_ids (lowercase
// hex). A DB error returns an empty set — the moderation gate fails
// open here so a transient DB hiccup never makes the public catalog
// disappear; the operator notices via the audit log and the
// `vigocli swarm poolq list` blocked count.
func (h *PoolqHandler) blockedSet(ctx context.Context) map[string]struct{} {
	out := map[string]struct{}{}
	if h.queries == nil {
		return out
	}
	rows, err := h.queries.ListBlockedPoolqTopics(ctx)
	if err != nil {
		return out
	}
	for _, r := range rows {
		out[strings.ToLower(r.TopicID)] = struct{}{}
	}
	return out
}

func (h *PoolqHandler) isBlocked(ctx context.Context, topicID string) bool {
	if h.queries == nil {
		return false
	}
	n, err := h.queries.IsPoolqTopicBlocked(ctx, topicID)
	return err == nil && n > 0
}

func (h *PoolqHandler) envoyInputs() []poolqmesh.EnvoyInput {
	if h.fleetIndex == nil {
		return nil
	}
	envoys := h.fleetIndex.AllEnvoys()
	out := make([]poolqmesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, poolqmesh.EnvoyInput{ID: e.ID, Hostname: e.Hostname, Revoked: e.Revoked})
	}
	return out
}

func (h *PoolqHandler) snapshot(r *http.Request) (*poolqmesh.Snapshot, error) {
	return h.agg.Snapshot(r.Context(), h.envoyInputs())
}

// FleetSnapshot returns the cached fleet snapshot plus the operator-block
// table. Passthrough for the UI handler — the `/swarm/poolq` page reads
// this via the [PoolqFleetSource] interface in `ui/swarm_poolq.go` so the
// ui package doesn't import poolqmesh + db directly. Matches the curator
// handler's `FleetSnapshot` shape.
func (h *PoolqHandler) FleetSnapshot(ctx context.Context) (*poolqmesh.Snapshot, []db.PoolqBlockedTopic, error) {
	snap, err := h.agg.Snapshot(ctx, h.envoyInputs())
	if err != nil {
		return nil, nil, err
	}
	blocked, _ := h.queries.ListBlockedPoolqTopics(ctx)
	if blocked == nil {
		blocked = []db.PoolqBlockedTopic{}
	}
	return snap, blocked, nil
}

// --- agent-facing (public) ------------------------------------------------

// topicSummary mirrors the head-only view of a Topic — no messages, just
// the per-topic stats. Cheaper than [poolqmesh.Topic] over the wire when
// the caller just wants to enumerate.
type topicSummary struct {
	TopicIDHex          string `json:"topic_id_hex"`
	TopicName           string `json:"topic_name"`
	FounderPuddlePubkey string `json:"founder_puddle_pubkey"`
	HeadSeq             uint64 `json:"head_seq"`
	Count               int    `json:"count"`
	FirstPostedAt       int64  `json:"first_posted_at"`
	LastPostedAt        int64  `json:"last_posted_at"`
	Blocked             bool   `json:"blocked"`
}

type topicsResponse struct {
	GeneratedAt  any            `json:"generated_at"`
	Topics       []topicSummary `json:"topics"`
	Participants []string       `json:"participants"`
	Blocked      []db.PoolqBlockedTopic `json:"blocked"`
}

func summarize(t poolqmesh.Topic, blocked map[string]struct{}) topicSummary {
	_, isBlocked := blocked[strings.ToLower(t.TopicIDHex)]
	return topicSummary{
		TopicIDHex:          t.TopicIDHex,
		TopicName:           t.TopicName,
		FounderPuddlePubkey: t.FounderPuddlePubkey,
		HeadSeq:             t.HeadSeq,
		Count:               t.Count,
		FirstPostedAt:       t.FirstPostedAt,
		LastPostedAt:        t.LastPostedAt,
		Blocked:             isBlocked,
	}
}

// handleTopics: GET /api/v1/swarm/poolq/topics
func (h *PoolqHandler) handleTopics(w http.ResponseWriter, r *http.Request) {
	snap, err := h.snapshot(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "building snapshot: "+err.Error())
		return
	}
	blocked := h.blockedSet(r.Context())
	out := topicsResponse{
		GeneratedAt:  snap.GeneratedAt,
		Topics:       make([]topicSummary, 0, len(snap.Topics)),
		Participants: snap.Participants,
	}
	for _, t := range snap.Topics {
		out.Topics = append(out.Topics, summarize(t, blocked))
	}
	if out.Participants == nil {
		out.Participants = []string{}
	}
	// Surface block rows without a current catalog entry too — the
	// operator should see them, otherwise blocks without messages look
	// like they did nothing. Matches the curator handleList shape.
	blockedRows, _ := h.queries.ListBlockedPoolqTopics(r.Context())
	if blockedRows == nil {
		blockedRows = []db.PoolqBlockedTopic{}
	}
	out.Blocked = blockedRows
	writeJSON(w, http.StatusOK, out)
}

// handleHead: GET /api/v1/swarm/poolq/topic/{topic_id}/head
// Returns the per-topic stats without the messages (cheap "is there new
// content?" probe for a polling consumer). Even a blocked topic returns
// its head (so a consumer learns the topic is blocked rather than seeing
// a 404); the range/log endpoints are what refuse.
func (h *PoolqHandler) handleHead(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("topic_id")))
	if !isHex64(id) {
		writeError(w, http.StatusBadRequest, "topic_id must be 64 hex characters")
		return
	}
	snap, err := h.snapshot(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "building snapshot: "+err.Error())
		return
	}
	t := snap.LookupByID(id)
	if t == nil {
		writeError(w, http.StatusNotFound, "no such topic")
		return
	}
	blocked := h.blockedSet(r.Context())
	writeJSON(w, http.StatusOK, summarize(*t, blocked))
}

type rangeResponse struct {
	TopicIDHex string             `json:"topic_id_hex"`
	From       uint64             `json:"from"`
	To         uint64             `json:"to"`
	Messages   []poolqmesh.Message `json:"messages"`
}

// handleRange: GET /api/v1/swarm/poolq/topic/{topic_id}/range?from=<seq>&to=<seq>
// `to=0` (or omitted) means "to the head". The consumer's read+advance
// loop is one call to this endpoint per cycle, then a local offset bump.
func (h *PoolqHandler) handleRange(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("topic_id")))
	if !isHex64(id) {
		writeError(w, http.StatusBadRequest, "topic_id must be 64 hex characters")
		return
	}
	from, err := parseSeqQuery(r, "from", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	to, err := parseSeqQuery(r, "to", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.isBlocked(r.Context(), id) {
		writeError(w, http.StatusNotFound, "topic is operator-blocked")
		return
	}
	snap, err := h.snapshot(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "building snapshot: "+err.Error())
		return
	}
	t := snap.LookupByID(id)
	if t == nil {
		writeError(w, http.StatusNotFound, "no such topic")
		return
	}
	msgs := poolqmesh.RangeMessages(t, from, to)
	if msgs == nil {
		msgs = []poolqmesh.Message{}
	}
	writeJSON(w, http.StatusOK, rangeResponse{
		TopicIDHex: id,
		From:       from,
		To:         to,
		Messages:   msgs,
	})
}

// handleLog: GET /api/v1/swarm/poolq/topic/{topic_id}/log
// Returns the full Topic with every message. Convenience for operator
// views; consumers in a steady state should use /range with offsets.
// Refuses on a blocked topic (matches curator's block semantics).
func (h *PoolqHandler) handleLog(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("topic_id")))
	if !isHex64(id) {
		writeError(w, http.StatusBadRequest, "topic_id must be 64 hex characters")
		return
	}
	if h.isBlocked(r.Context(), id) {
		writeError(w, http.StatusNotFound, "topic is operator-blocked")
		return
	}
	snap, err := h.snapshot(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "building snapshot: "+err.Error())
		return
	}
	t := snap.LookupByID(id)
	if t == nil {
		writeError(w, http.StatusNotFound, "no such topic")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// --- admin moderation -----------------------------------------------------

// handleBlock: POST /api/v1/swarm/poolq/block/{topic_id}?reason=<text>
func (h *PoolqHandler) handleBlock(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("topic_id")))
	if !isHex64(id) {
		writeError(w, http.StatusBadRequest, "topic_id must be 64 hex characters")
		return
	}
	actor := ""
	if u := auth.UserFromContext(r.Context()); u != nil {
		actor = u.Username
	}
	if err := h.queries.BlockPoolqTopic(r.Context(), db.BlockPoolqTopicParams{
		TopicID:   id,
		BlockedBy: actor,
		Reason:    strings.TrimSpace(r.URL.Query().Get("reason")),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "blocking topic: "+err.Error())
		return
	}
	// The snapshot cache doesn't gate the `isBlocked` check (DB roundtrip on
	// every range/log call) but invalidating keeps any rendered "is this
	// blocked?" column on /topics synchronously consistent with the block we
	// just wrote.
	h.agg.InvalidateCache()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "topic_id": id, "blocked": true})
}

// handleUnblock: POST /api/v1/swarm/poolq/unblock/{topic_id}
func (h *PoolqHandler) handleUnblock(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("topic_id")))
	if !isHex64(id) {
		writeError(w, http.StatusBadRequest, "topic_id must be 64 hex characters")
		return
	}
	if err := h.queries.UnblockPoolqTopic(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "unblocking topic: "+err.Error())
		return
	}
	h.agg.InvalidateCache()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "topic_id": id, "blocked": false})
}

func parseSeqQuery(r *http.Request, name string, def uint64) (uint64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, &httpBadRequestError{msg: name + " must be a non-negative integer"}
	}
	return v, nil
}

type httpBadRequestError struct{ msg string }

func (e *httpBadRequestError) Error() string { return e.msg }
