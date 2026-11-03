// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/swarm/poolqmesh"
)

// mockPoolq implements [PoolqFleetSource] with injectable snapshots so
// the handler can be exercised without standing up the real aggregator + DB.
type mockPoolq struct {
	snap    *poolqmesh.Snapshot
	blocked []db.PoolqBlockedTopic
	err     error
}

func (m *mockPoolq) FleetSnapshot(_ context.Context) (*poolqmesh.Snapshot, []db.PoolqBlockedTopic, error) {
	return m.snap, m.blocked, m.err
}

func poolqPubkeyHex(seed byte) string {
	var b [32]byte
	for i := range b {
		b[i] = seed
	}
	return hex.EncodeToString(b[:])
}

func samplePoolqTopic(seed byte, name string, head uint64) poolqmesh.Topic {
	author := poolqPubkeyHex(seed)
	tid, _ := poolqmesh.TopicIDFor(author, name)
	msgs := make([]poolqmesh.Message, 0, head)
	for i := uint64(1); i <= head; i++ {
		msgs = append(msgs, poolqmesh.Message{
			Body: poolqmesh.MessageBody{
				SchemaVersion:      1,
				TopicName:          name,
				Seq:                i,
				Body:               "msg " + string(rune('A'+int(i-1))),
				PostedAt:           int64(1_700_000_000_000) + int64(i)*1000,
				AuthorPuddlePubkey: author,
			},
			Signature: strings.Repeat("0", 128),
		})
	}
	return poolqmesh.Topic{
		TopicIDHex:          tid,
		TopicName:           name,
		FounderPuddlePubkey: author,
		HeadSeq:             head,
		Count:               int(head),
		FirstPostedAt:       int64(1_700_000_000_000) + 1000,
		LastPostedAt:        int64(1_700_000_000_000) + int64(head)*1000,
		Messages:            msgs,
	}
}

// TestPoolq_RBAC pins admin-only on both the list and detail pages.
// Viewers must never see message bodies + founder pubkeys.
func TestPoolq_RBAC(t *testing.T) {
	h := newTestHandler(t)
	h.authEnabled = true
	h.poolqFleet = &mockPoolq{snap: &poolqmesh.Snapshot{}}

	for _, path := range []string{"/swarm/poolq", "/swarm/poolq/" + strings.Repeat("a", 64)} {
		rec := httptest.NewRecorder()
		req := viewerRequest(http.MethodGet, path)
		if strings.Count(path, "/") > 2 {
			req.SetPathValue("topic_id", path[len("/swarm/poolq/"):])
		}
		h.Poolq(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("viewer GET %s = %d, want 403", path, rec.Code)
		}
	}
}

func TestPoolq_ListEmpty(t *testing.T) {
	h := newTestHandler(t)
	h.poolqFleet = &mockPoolq{snap: &poolqmesh.Snapshot{}}
	rec := httptest.NewRecorder()
	h.Poolq(rec, adminRequest(http.MethodGet, "/swarm/poolq"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No poolq topics observed") {
		t.Errorf("expected empty-state message, got: %s", body)
	}
}

func TestPoolq_ListRendersTopicsAndOrphanFooter(t *testing.T) {
	h := newTestHandler(t)
	live := samplePoolqTopic(0x01, "build-tasks", 3)
	phantom := strings.Repeat("9", 64)
	h.poolqFleet = &mockPoolq{
		snap: &poolqmesh.Snapshot{
			GeneratedAt:  time.Unix(1714521600, 0),
			Topics:       []poolqmesh.Topic{live},
			Participants: []string{"plex", "danlap"},
		},
		blocked: []db.PoolqBlockedTopic{
			{TopicID: phantom, BlockedAt: time.Unix(1714521000, 0), BlockedBy: "dan", Reason: "spam"},
		},
	}
	rec := httptest.NewRecorder()
	h.Poolq(rec, adminRequest(http.MethodGet, "/swarm/poolq"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Live row.
	if !strings.Contains(body, "build-tasks") {
		t.Errorf("expected live topic row, got: %s", body)
	}
	if !strings.Contains(body, live.TopicIDHex[:12]) {
		t.Errorf("expected truncated live topic_id in body")
	}
	// Orphan footer.
	if !strings.Contains(body, "Operator blocks without a current topic") {
		t.Errorf("expected orphan-blocks footer, got: %s", body)
	}
	if !strings.Contains(body, phantom) {
		t.Errorf("expected phantom topic_id in orphan footer")
	}
	if !strings.Contains(body, "spam") {
		t.Errorf("expected reason in orphan footer")
	}
}

func TestPoolq_ListMarksBlockedLiveTopic(t *testing.T) {
	h := newTestHandler(t)
	live := samplePoolqTopic(0x01, "blocked-topic", 1)
	h.poolqFleet = &mockPoolq{
		snap:    &poolqmesh.Snapshot{Topics: []poolqmesh.Topic{live}},
		blocked: []db.PoolqBlockedTopic{{TopicID: live.TopicIDHex, BlockedBy: "dan"}},
	}
	rec := httptest.NewRecorder()
	h.Poolq(rec, adminRequest(http.MethodGet, "/swarm/poolq"))
	body := rec.Body.String()
	if !strings.Contains(body, ">blocked<") {
		t.Errorf("expected blocked badge on the live row, got: %s", body)
	}
	// A live blocked topic must NOT appear in the orphan-blocks footer.
	if strings.Contains(body, "Operator blocks without a current topic") {
		t.Errorf("a live blocked topic should not be in the orphan footer")
	}
}

func TestPoolq_DetailByHexID(t *testing.T) {
	h := newTestHandler(t)
	topic := samplePoolqTopic(0x01, "build-tasks", 3)
	h.poolqFleet = &mockPoolq{snap: &poolqmesh.Snapshot{Topics: []poolqmesh.Topic{topic}}}

	rec := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/swarm/poolq/"+topic.TopicIDHex)
	req.SetPathValue("topic_id", topic.TopicIDHex)
	h.Poolq(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "build-tasks") {
		t.Errorf("expected name in detail body, got: %s", body)
	}
	if !strings.Contains(body, topic.TopicIDHex) {
		t.Errorf("expected full topic_id in detail body")
	}
	// All three message bodies render.
	for _, want := range []string{"msg A", "msg B", "msg C"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in detail body", want)
		}
	}
}

func TestPoolq_DetailByNameFallback(t *testing.T) {
	h := newTestHandler(t)
	topic := samplePoolqTopic(0x01, "thing", 1)
	h.poolqFleet = &mockPoolq{snap: &poolqmesh.Snapshot{Topics: []poolqmesh.Topic{topic}}}

	rec := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/swarm/poolq/thing")
	req.SetPathValue("topic_id", "thing")
	h.Poolq(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "thing") {
		t.Errorf("expected name-fallback to resolve")
	}
}

func TestPoolq_DetailNotFound(t *testing.T) {
	h := newTestHandler(t)
	h.poolqFleet = &mockPoolq{snap: &poolqmesh.Snapshot{}}

	rec := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/swarm/poolq/"+strings.Repeat("0", 64))
	req.SetPathValue("topic_id", strings.Repeat("0", 64))
	h.Poolq(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown topic, got %d", rec.Code)
	}
}

func TestPoolq_DetailStampsBlockedFromDB(t *testing.T) {
	h := newTestHandler(t)
	topic := samplePoolqTopic(0x01, "build-tasks", 1)
	// Snapshot has Blocked=false (the aggregator never sets it); the
	// detail handler must stamp it from the DB-side blocked table.
	h.poolqFleet = &mockPoolq{
		snap:    &poolqmesh.Snapshot{Topics: []poolqmesh.Topic{topic}},
		blocked: []db.PoolqBlockedTopic{{TopicID: topic.TopicIDHex, BlockedBy: "dan", Reason: "spam"}},
	}
	rec := httptest.NewRecorder()
	req := adminRequest(http.MethodGet, "/swarm/poolq/"+topic.TopicIDHex)
	req.SetPathValue("topic_id", topic.TopicIDHex)
	h.Poolq(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "blocked by the operator") {
		t.Errorf("expected blocked-status row on the detail page, got: %s", body)
	}
}
