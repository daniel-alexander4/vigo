// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"sync"
	"testing"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
)

// recordingAudit captures emitted audit events so tests can assert
// what fired without coupling to the production *audit.Writer.
type recordingAudit struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	eventType, actor, source, summary, payload string
}

func (r *recordingAudit) Record(_ context.Context, eventType, actor, source, summary, payload string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedEvent{eventType, actor, source, summary, payload})
	return nil
}

func (r *recordingAudit) events_(eventType string) []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []recordedEvent
	for _, e := range r.events {
		if e.eventType == eventType {
			out = append(out, e)
		}
	}
	return out
}

func newHubForPinTests(t *testing.T) (*Hub, *recordingAudit) {
	t.Helper()
	h := &Hub{
		bolts:       nil,
		boltReports: make(map[string]*pb.BoltStatusReport),
		boltPins:    make(map[string]*BoltPin),
	}
	a := &recordingAudit{}
	h.SetAuditWriter(a)
	return h, a
}

// signReport mirrors the reporter's signing path for test fixtures.
func signReport(t *testing.T, report *pb.BoltStatusReport, priv ed25519.PrivateKey) {
	t.Helper()
	canonical, err := marshalReportContent(report)
	if err != nil {
		t.Fatalf("marshalReportContent: %v", err)
	}
	report.BoltPubkey = []byte(priv.Public().(ed25519.PublicKey))
	report.Signature = ed25519.Sign(priv, canonical)
}

func TestRecordBoltReport_LegacyAcceptedWithoutPinning(t *testing.T) {
	h, audit := newHubForPinTests(t)
	report := &pb.BoltStatusReport{
		BoltId:     "dc1",
		EnvoyCount: 5,
	}
	if err := h.RecordBoltReport(context.Background(), report); err != nil {
		t.Fatalf("legacy report rejected: %v", err)
	}
	if len(h.boltPins) != 0 {
		t.Errorf("legacy report should not have pinned anything; got %d pins", len(h.boltPins))
	}
	if len(audit.events_("spanner_bolt_pubkey_mismatch")) != 0 {
		t.Errorf("legacy report should not emit mismatch audit events")
	}
}

func TestRecordBoltReport_FirstSightingPins(t *testing.T) {
	h, audit := newHubForPinTests(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	report := &pb.BoltStatusReport{
		BoltId:     "dc1",
		SpannerId:  "alexander4-prod",
		EnvoyCount: 5,
	}
	signReport(t, report, priv)

	if err := h.RecordBoltReport(context.Background(), report); err != nil {
		t.Fatalf("first sighting rejected: %v", err)
	}
	pin := h.LookupBoltPin("alexander4-prod", "dc1")
	if pin == nil {
		t.Fatal("expected pin after first sighting")
	}
	if !bytesEqual(pin.Pubkey, []byte(priv.Public().(ed25519.PublicKey))) {
		t.Error("pinned pubkey does not match asserted pubkey")
	}
	if len(audit.events_("spanner_bolt_pubkey_mismatch")) != 0 {
		t.Error("first sighting should not emit mismatch event")
	}
}

func TestRecordBoltReport_SameKeyAcceptedTwice(t *testing.T) {
	h, _ := newHubForPinTests(t)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	for i := 0; i < 2; i++ {
		report := &pb.BoltStatusReport{
			BoltId:     "dc1",
			SpannerId:  "alexander4-prod",
			EnvoyCount: int32(i),
		}
		signReport(t, report, priv)
		if err := h.RecordBoltReport(context.Background(), report); err != nil {
			t.Fatalf("report %d rejected: %v", i, err)
		}
	}
}

func TestRecordBoltReport_MismatchRejected(t *testing.T) {
	h, audit := newHubForPinTests(t)
	_, priv1, _ := ed25519.GenerateKey(rand.Reader)
	_, priv2, _ := ed25519.GenerateKey(rand.Reader)

	// First sighting pins priv1.
	r1 := &pb.BoltStatusReport{
		BoltId: "dc1", SpannerId: "alexander4-prod", EnvoyCount: 5,
	}
	signReport(t, r1, priv1)
	if err := h.RecordBoltReport(context.Background(), r1); err != nil {
		t.Fatalf("first sighting rejected: %v", err)
	}

	// Second report signed by priv2 — must be rejected.
	r2 := &pb.BoltStatusReport{
		BoltId: "dc1", SpannerId: "alexander4-prod", EnvoyCount: 6,
	}
	signReport(t, r2, priv2)
	err := h.RecordBoltReport(context.Background(), r2)
	if err == nil {
		t.Fatal("mismatched-key report should be rejected")
	}
	if !strings.Contains(err.Error(), "does not match pinned identity") {
		t.Errorf("expected pinned-identity error, got: %v", err)
	}
	// Mismatch audit event should fire.
	mismatch := audit.events_("spanner_bolt_pubkey_mismatch")
	if len(mismatch) != 1 {
		t.Fatalf("expected 1 mismatch event, got %d", len(mismatch))
	}
	if !strings.Contains(mismatch[0].payload, `"decision":"rejected"`) {
		t.Errorf("audit payload missing rejected decision: %s", mismatch[0].payload)
	}

	// Pinned report should NOT be replaced by the rejected one.
	stored, ok := h.boltReports["dc1"]
	if !ok {
		t.Fatal("first-sighting report should remain after mismatch reject")
	}
	if stored.EnvoyCount != 5 {
		t.Errorf("stored report mutated after rejection: EnvoyCount = %d, want 5", stored.EnvoyCount)
	}
}

func TestRecordBoltReport_BadSignatureRejected(t *testing.T) {
	h, audit := newHubForPinTests(t)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	report := &pb.BoltStatusReport{
		BoltId: "dc1", SpannerId: "alexander4-prod", EnvoyCount: 5,
	}
	signReport(t, report, priv)
	// Corrupt the signature.
	report.Signature[0] ^= 0xff

	err := h.RecordBoltReport(context.Background(), report)
	if err == nil {
		t.Fatal("bad-signature report should be rejected")
	}
	if !strings.Contains(err.Error(), "signature verify") {
		t.Errorf("expected signature-verify error, got: %v", err)
	}
	if len(audit.events_("spanner_bolt_pubkey_mismatch")) != 1 {
		t.Error("bad-signature reject should emit mismatch audit event")
	}
}

func TestRecordBoltReport_DistinctSpannersDoNotCollide(t *testing.T) {
	// Two spanners can each have a bolt named "dc1" without
	// the pins colliding.
	h, _ := newHubForPinTests(t)
	_, p1, _ := ed25519.GenerateKey(rand.Reader)
	_, p2, _ := ed25519.GenerateKey(rand.Reader)

	r1 := &pb.BoltStatusReport{BoltId: "dc1", SpannerId: "spanner-a"}
	signReport(t, r1, p1)
	r2 := &pb.BoltStatusReport{BoltId: "dc1", SpannerId: "spanner-b"}
	signReport(t, r2, p2)

	if err := h.RecordBoltReport(context.Background(), r1); err != nil {
		t.Fatalf("spanner-a report rejected: %v", err)
	}
	if err := h.RecordBoltReport(context.Background(), r2); err != nil {
		t.Fatalf("spanner-b report rejected: %v", err)
	}
	if pin := h.LookupBoltPin("spanner-a", "dc1"); pin == nil || !bytesEqual(pin.Pubkey, []byte(p1.Public().(ed25519.PublicKey))) {
		t.Error("spanner-a pin should hold p1's pubkey")
	}
	if pin := h.LookupBoltPin("spanner-b", "dc1"); pin == nil || !bytesEqual(pin.Pubkey, []byte(p2.Public().(ed25519.PublicKey))) {
		t.Error("spanner-b pin should hold p2's pubkey")
	}
}

func TestRecordBoltReport_SignedReportRequiresSpannerID(t *testing.T) {
	h, _ := newHubForPinTests(t)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	report := &pb.BoltStatusReport{BoltId: "dc1"} // SpannerId omitted
	signReport(t, report, priv)
	err := h.RecordBoltReport(context.Background(), report)
	if err == nil || !strings.Contains(err.Error(), "missing spanner_id") {
		t.Errorf("expected missing-spanner_id error, got: %v", err)
	}
}
