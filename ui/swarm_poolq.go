// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"fmt"
	"net/http"
	"strings"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/i18n"
	"git.alexander4.io/vigo/server/swarm/poolqmesh"
)

// PoolqPageData drives `ui/templates/pages/poolq.html`. Carries both the
// list view and the per-topic detail view; the template picks which to
// render based on whether `Topic` is non-nil. The `Blocked` table is only
// consulted on the list page (footer for orphan blocks); per-topic
// blocked status comes from joining `topicID` against `Blocked` in the
// list template, or from `Topic.Blocked` on the detail page.
//
// No view-builder layer between this struct and `poolqmesh.Topic`: poolq
// messages are already seq-sorted by the mesh aggregator and have no
// cross-cuts to compute (curator's view-builder exists for tag inversion
// + dangling detection + rekey logic — none of which apply here).
type PoolqPageData struct {
	PageData
	Health  HealthBanner
	Snap    *poolqmesh.Snapshot
	Blocked []db.PoolqBlockedTopic
	Topic   *poolqmesh.Topic // non-nil for the detail page
	Error   string
}

// Poolq serves `GET /swarm/poolq` (list) and
// `GET /swarm/poolq/{topic_id}` (detail). Admin-only — the fleet topic
// catalog exposes founder puddle pubkeys and message bodies that aren't
// viewer-appropriate. (Reading individual topics is ungated through
// `vigo swarm poolq read`; this is the fleet-aggregator view.)
//
// `{topic_id}` accepts the canonical 64-char-lowercase-hex form OR a
// unique topic-name match across the fleet (UX convenience — the
// cryptographic identity remains the topic_id).
func (h *Handler) Poolq(w http.ResponseWriter, r *http.Request) {
	if !h.adminOnly(w, r) {
		return
	}
	id := r.PathValue("topic_id")
	if id != "" {
		h.poolqDetail(w, r, id)
		return
	}
	h.poolqList(w, r)
}

func (h *Handler) poolqList(w http.ResponseWriter, r *http.Request) {
	data := PoolqPageData{
		PageData: PageData{Title: "Poolq", ActiveNav: "poolq"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.poolqFleet == nil {
		data.Error = i18n.CatalogT(data.Locale, "health_banner.mesh_unavailable")
		data.Health = finalizeHealthBanner(data.Locale, "Poolq", nil, i18n.CatalogT(data.Locale, "health_banner.aggregator_unavailable"))
		data.Health.Level = "warn"
		data.Health.Locale = data.Locale
		h.renderer.Render(w, "poolq", data)
		return
	}

	snap, blocked, err := h.poolqFleet.FleetSnapshot(r.Context())
	if err != nil {
		data.Error = err.Error()
		data.Health = finalizeHealthBanner(data.Locale, "Poolq", nil, i18n.CatalogT(data.Locale, "health_banner.snapshot_failed"))
		data.Health.Level = "warn"
		data.Health.Locale = data.Locale
		h.renderer.Render(w, "poolq", data)
		return
	}
	data.Snap = snap
	data.Blocked = blocked
	data.Health = buildPoolqHealth(data.Locale, snap, blocked)
	data.Health.Locale = data.Locale
	h.renderer.Render(w, "poolq", data)
}

func (h *Handler) poolqDetail(w http.ResponseWriter, r *http.Request, id string) {
	data := PoolqPageData{
		PageData: PageData{Title: "Poolq", ActiveNav: "poolq"},
	}
	h.setCommonWithRequest(&data.PageData, r)

	if h.poolqFleet == nil {
		http.Error(w, "poolq aggregator unavailable", http.StatusServiceUnavailable)
		return
	}
	snap, blocked, err := h.poolqFleet.FleetSnapshot(r.Context())
	if err != nil {
		http.Error(w, "snapshot failed", http.StatusInternalServerError)
		return
	}
	// Hex id wins; name lookup is a fallback so an operator who typed a
	// name into the URL bar doesn't 404.
	topic := snap.LookupByID(id)
	matchedID := strings.ToLower(id)
	if topic == nil {
		topic, matchedID = lookupPoolqByName(snap, id)
	}
	if topic == nil {
		http.NotFound(w, r)
		return
	}
	// Stamp the operator-block flag from the DB-side lookup. The
	// aggregator's Topic.Blocked is zero-valued; the source of truth for
	// blocking is the `poolq_blocked_topics` table, not the snapshot.
	view := *topic
	for _, b := range blocked {
		if strings.EqualFold(b.TopicID, matchedID) {
			view.Blocked = true
			break
		}
	}
	data.Topic = &view
	data.Health = buildPoolqTopicHealth(data.Locale, &view)
	data.Health.Locale = data.Locale
	h.renderer.Render(w, "poolq", data)
}

// lookupPoolqByName resolves a bare topic-name reference: exact match
// across the fleet snapshot, then unique-prefix match. Returns the
// matched Topic and the canonical lowercase topic_id; nil + empty on a
// miss or a name collision (two founders publishing the same name —
// operator pastes the hex id from the list page to disambiguate).
func lookupPoolqByName(snap *poolqmesh.Snapshot, target string) (*poolqmesh.Topic, string) {
	if snap == nil || len(snap.Topics) == 0 {
		return nil, ""
	}
	var exact []*poolqmesh.Topic
	for i := range snap.Topics {
		if snap.Topics[i].TopicName == target {
			exact = append(exact, &snap.Topics[i])
		}
	}
	if len(exact) == 1 {
		return exact[0], strings.ToLower(exact[0].TopicIDHex)
	}
	if len(exact) > 1 {
		return nil, ""
	}
	var prefix []*poolqmesh.Topic
	for i := range snap.Topics {
		if strings.HasPrefix(snap.Topics[i].TopicName, target) {
			prefix = append(prefix, &snap.Topics[i])
		}
	}
	if len(prefix) == 1 {
		return prefix[0], strings.ToLower(prefix[0].TopicIDHex)
	}
	return nil, ""
}

// buildPoolqHealth flags blocked topics and orphan blocks (block rows
// whose topic isn't in the current snapshot — typically the founder
// scrubbed `~/.vigo-poolq/<topic_id>/` directly). Mirrors the curator
// health-builder's shape.
func buildPoolqHealth(locale string, snap *poolqmesh.Snapshot, blocked []db.PoolqBlockedTopic) HealthBanner {
	if snap == nil {
		return finalizeHealthBanner(locale, "Poolq", nil, i18n.CatalogT(locale, "health_banner.no_data"))
	}
	var problems []HealthProblem
	liveIDs := make(map[string]string, len(snap.Topics)) // lower-hex → name
	for i := range snap.Topics {
		liveIDs[strings.ToLower(snap.Topics[i].TopicIDHex)] = snap.Topics[i].TopicName
	}
	var orphans []db.PoolqBlockedTopic
	for _, b := range blocked {
		bid := strings.ToLower(b.TopicID)
		if name, ok := liveIDs[bid]; ok {
			problems = append(problems, HealthProblem{
				Kind:     "blocked",
				Subject:  name,
				Detail:   poolqBlockReasonText(b),
				Link:     "/swarm/poolq/" + bid,
				Severity: "warn",
			})
		} else {
			orphans = append(orphans, b)
		}
	}
	if n := len(orphans); n > 0 {
		problems = append(problems, HealthProblem{
			Kind:     "blocked-orphan",
			Subject:  fmt.Sprintf("%d topic_id(s)", n),
			Detail:   i18n.CatalogT(locale, "swarm.poolq.orphans_detail"),
			Severity: "warn",
		})
	}

	totalMsgs := 0
	for i := range snap.Topics {
		totalMsgs += snap.Topics[i].Count
	}
	headline := fmt.Sprintf(
		i18n.CatalogT(locale, "swarm.poolq.headline"),
		len(snap.Topics),
		totalMsgs,
		len(snap.Participants),
		len(blocked),
	)
	return finalizeHealthBanner(locale, "Poolq", problems, headline)
}

func buildPoolqTopicHealth(locale string, t *poolqmesh.Topic) HealthBanner {
	if t == nil {
		return finalizeHealthBanner(locale, "Poolq", nil, i18n.CatalogT(locale, "health_banner.no_data"))
	}
	var problems []HealthProblem
	if t.Blocked {
		problems = append(problems, HealthProblem{
			Kind:     "blocked",
			Subject:  t.TopicName,
			Detail:   i18n.CatalogT(locale, "swarm.poolq.topic_blocked_detail"),
			Severity: "warn",
		})
	}
	headline := fmt.Sprintf(i18n.CatalogT(locale, "swarm.poolq.topic_headline"), t.TopicName, t.Count, t.HeadSeq)
	return finalizeHealthBanner(locale, t.TopicName, problems, headline)
}

func poolqBlockReasonText(b db.PoolqBlockedTopic) string {
	if b.Reason != "" {
		return b.Reason
	}
	return "operator-blocked"
}
