// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package poolqmesh

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// pk32 returns a deterministic 32-byte hex pubkey from a seed byte.
func pk32(seed byte) string {
	var b [32]byte
	for i := range b {
		b[i] = seed
	}
	return hex.EncodeToString(b[:])
}

// wantTopicID is the reference topic_id for (authorHex, topicName).
func wantTopicID(t *testing.T, authorHex, topicName string) string {
	t.Helper()
	raw, err := hex.DecodeString(authorHex)
	if err != nil {
		t.Fatalf("bad author hex: %v", err)
	}
	h := sha256.New()
	h.Write(raw)
	h.Write([]byte(topicName))
	return hex.EncodeToString(h.Sum(nil))
}

func mkMessage(authorHex, topicName string, seq uint64, body string, postedAt int64) Message {
	return Message{
		Body: MessageBody{
			SchemaVersion:      1,
			TopicName:          topicName,
			Seq:                seq,
			Body:               body,
			PostedAt:           postedAt,
			AuthorPuddlePubkey: authorHex,
		},
		// Signature flows through verbatim; the server never validates it.
		Signature: hex.EncodeToString(make([]byte, 64)),
	}
}

func envelope(msgs ...Message) *poolqTraitEnvelope {
	return &poolqTraitEnvelope{
		UpdatedAt:    1,
		MessageCount: len(msgs),
		Messages:     msgs,
	}
}

func TestTopicIDFor(t *testing.T) {
	author := pk32(0x11)
	got, err := TopicIDFor(author, "build-tasks")
	if err != nil {
		t.Fatalf("TopicIDFor: %v", err)
	}
	if got != wantTopicID(t, author, "build-tasks") {
		t.Fatalf("topic_id mismatch: got %s", got)
	}
	// Bad inputs surface as errors, not silent zero-hashes.
	if _, err := TopicIDFor("not-hex", "x"); err == nil {
		t.Fatalf("expected error for non-hex author")
	}
	if _, err := TopicIDFor("aabb", "x"); err == nil {
		t.Fatalf("expected error for short pubkey")
	}
}

func TestAggregateBucketsByDerivedTopicID(t *testing.T) {
	founderA := pk32(0xA1)
	founderB := pk32(0xB2)

	// Two founders, both posting under topic_name="build-tasks". They land in
	// different buckets (different topic_ids) — this IS v1 founder-only
	// enforcement: founder B simply creates a parallel topic it owns rather
	// than spoofing founder A's.
	rows := []envelopeRow{
		{hostname: "plex", envelope: envelope(
			mkMessage(founderA, "build-tasks", 1, "from-A-1", 1_700_000_000_000),
			mkMessage(founderA, "build-tasks", 2, "from-A-2", 1_700_000_001_000),
		)},
		{hostname: "annlap", envelope: envelope(
			mkMessage(founderB, "build-tasks", 1, "from-B-1", 1_700_000_002_000),
		)},
	}
	// cutoff far in the past so nothing prunes.
	snap := aggregate(rows, 0)
	if got := len(snap.Topics); got != 2 {
		t.Fatalf("expected 2 topics, got %d", got)
	}
	tidA := wantTopicID(t, founderA, "build-tasks")
	tidB := wantTopicID(t, founderB, "build-tasks")
	if a := snap.LookupByID(tidA); a == nil || a.Count != 2 {
		t.Fatalf("founder A topic missing or wrong count")
	}
	if b := snap.LookupByID(tidB); b == nil || b.Count != 1 {
		t.Fatalf("founder B topic missing or wrong count")
	}
	// Participants list collects every contributing hostname.
	if len(snap.Participants) != 2 {
		t.Fatalf("expected 2 participants, got %v", snap.Participants)
	}
}

func TestAggregateMergesGSetByMsgID(t *testing.T) {
	// Two envoys reporting THE SAME signed message — must collapse to one
	// entry (G-Set union by msg_id, which derives from canonical bytes).
	author := pk32(0x42)
	m := mkMessage(author, "build-tasks", 1, "hello", 1_700_000_000_000)
	rows := []envelopeRow{
		{hostname: "plex", envelope: envelope(m)},
		{hostname: "annlap", envelope: envelope(m)},
	}
	snap := aggregate(rows, 0)
	if len(snap.Topics) != 1 {
		t.Fatalf("expected 1 topic, got %d", len(snap.Topics))
	}
	if got := snap.Topics[0].Count; got != 1 {
		t.Fatalf("expected 1 message after G-Set merge, got %d", got)
	}
}

func TestAggregateOrdersBySeq(t *testing.T) {
	author := pk32(0x42)
	// Insert out of order; the snapshot must put them back in seq order.
	rows := []envelopeRow{
		{hostname: "plex", envelope: envelope(
			mkMessage(author, "build-tasks", 3, "three", 1_700_000_003_000),
			mkMessage(author, "build-tasks", 1, "one", 1_700_000_001_000),
			mkMessage(author, "build-tasks", 2, "two", 1_700_000_002_000),
		)},
	}
	snap := aggregate(rows, 0)
	topic := &snap.Topics[0]
	got := []uint64{
		topic.Messages[0].Body.Seq,
		topic.Messages[1].Body.Seq,
		topic.Messages[2].Body.Seq,
	}
	want := []uint64{1, 2, 3}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("messages not seq-sorted: got %v, want %v", got, want)
		}
	}
	if topic.HeadSeq != 3 {
		t.Fatalf("HeadSeq = %d, want 3", topic.HeadSeq)
	}
	if topic.FirstPostedAt != 1_700_000_001_000 || topic.LastPostedAt != 1_700_000_003_000 {
		t.Fatalf("posted-at summary wrong: first=%d last=%d", topic.FirstPostedAt, topic.LastPostedAt)
	}
}

func TestAgePruneAndStaleOnIngestRefusal(t *testing.T) {
	author := pk32(0x42)
	// cutoff at posted_at = 1_700_000_002_000 — anything strictly older is
	// dropped. This single predicate is both stale-on-ingest refusal AND
	// age-prune (what makes the model flap-stable).
	cutoff := int64(1_700_000_002_000)
	rows := []envelopeRow{
		{hostname: "plex", envelope: envelope(
			mkMessage(author, "build-tasks", 1, "stale", 1_700_000_001_000),
			mkMessage(author, "build-tasks", 2, "boundary", cutoff),
			mkMessage(author, "build-tasks", 3, "fresh", 1_700_000_003_000),
		)},
	}
	snap := aggregate(rows, cutoff)
	if len(snap.Topics) != 1 {
		t.Fatalf("expected 1 topic, got %d", len(snap.Topics))
	}
	got := snap.Topics[0]
	if got.Count != 2 {
		t.Fatalf("expected 2 messages after prune (boundary kept, stale dropped), got %d", got.Count)
	}
	if got.Messages[0].Body.Seq != 2 || got.Messages[1].Body.Seq != 3 {
		t.Fatalf("retained wrong messages: %+v", got.Messages)
	}
}

func TestAggregateDropsRowsWithBadPubkey(t *testing.T) {
	good := pk32(0x33)
	// One good row + one row with malformed author_pubkey → only the good
	// one shows up, no panic.
	rows := []envelopeRow{
		{hostname: "plex", envelope: envelope(
			mkMessage(good, "build-tasks", 1, "ok", 1_700_000_000_000),
		)},
		{hostname: "annlap", envelope: envelope(
			Message{
				Body: MessageBody{
					SchemaVersion: 1, TopicName: "build-tasks", Seq: 1,
					Body: "junk", PostedAt: 1_700_000_001_000,
					AuthorPuddlePubkey: "not-hex",
				},
				Signature: hex.EncodeToString(make([]byte, 64)),
			},
		)},
	}
	snap := aggregate(rows, 0)
	if len(snap.Topics) != 1 {
		t.Fatalf("expected 1 topic, got %d", len(snap.Topics))
	}
}

func TestRangeMessagesSlicesByContiguousSeq(t *testing.T) {
	author := pk32(0x42)
	rows := []envelopeRow{
		{hostname: "plex", envelope: envelope(
			mkMessage(author, "build-tasks", 1, "one", 1_700_000_000_001),
			mkMessage(author, "build-tasks", 2, "two", 1_700_000_000_002),
			mkMessage(author, "build-tasks", 3, "three", 1_700_000_000_003),
			mkMessage(author, "build-tasks", 4, "four", 1_700_000_000_004),
			mkMessage(author, "build-tasks", 5, "five", 1_700_000_000_005),
		)},
	}
	snap := aggregate(rows, 0)
	topic := &snap.Topics[0]
	// [2, 4] should return seqs 2,3,4.
	r := RangeMessages(topic, 2, 4)
	if len(r) != 3 || r[0].Body.Seq != 2 || r[2].Body.Seq != 4 {
		t.Fatalf("range [2,4] wrong: %+v", r)
	}
	// to=0 means "to head"; from=3 should return 3,4,5.
	r = RangeMessages(topic, 3, 0)
	if len(r) != 3 || r[0].Body.Seq != 3 || r[2].Body.Seq != 5 {
		t.Fatalf("range [3,head] wrong: %+v", r)
	}
	// from past head returns nil.
	if RangeMessages(topic, 100, 0) != nil {
		t.Fatalf("expected nil for from-past-head")
	}
	// nil/empty topic returns nil.
	if RangeMessages(nil, 0, 0) != nil {
		t.Fatalf("expected nil for nil topic")
	}
}

func TestLookupByID(t *testing.T) {
	author := pk32(0x42)
	rows := []envelopeRow{
		{hostname: "plex", envelope: envelope(
			mkMessage(author, "build-tasks", 1, "hi", 1_700_000_000_000),
		)},
	}
	snap := aggregate(rows, 0)
	tid := wantTopicID(t, author, "build-tasks")
	// Hot path uses byID.
	if got := snap.LookupByID(tid); got == nil || got.TopicName != "build-tasks" {
		t.Fatalf("LookupByID via byID failed")
	}
	// Hand-built snapshot with no byID still works via fallback scan.
	manual := &Snapshot{Topics: snap.Topics}
	if got := manual.LookupByID(tid); got == nil {
		t.Fatalf("LookupByID fallback scan failed")
	}
	// Mixed case input is normalized.
	if got := snap.LookupByID(uppercase(tid)); got == nil {
		t.Fatalf("LookupByID is case-sensitive")
	}
	// Unknown id returns nil.
	if got := snap.LookupByID(wantTopicID(t, pk32(0xFF), "nope")); got != nil {
		t.Fatalf("expected nil for unknown id")
	}
}

func uppercase(s string) string {
	out := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'a' && c <= 'f' {
			c -= 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// TestInteropMsgIDPin is the cross-language pin. The same MessageBody
// fields are pinned to the same hex msg_id in
// agent/src/swarm/poolq/message.rs's `interop_pin_msg_id_matches_server_side`.
// If either implementation's canonical-bytes layout drifts (re-ordering
// fields, changing length-prefix widths, changing the domain string),
// exactly one of the two tests fails before the drift can land in a
// release.
func TestInteropMsgIDPin(t *testing.T) {
	body := MessageBody{
		SchemaVersion:      1,
		TopicName:          "build-tasks",
		Seq:                42,
		Body:               "hello",
		PostedAt:           1_700_000_000_123,
		AuthorPuddlePubkey: hex.EncodeToString(bytesOf(0x42, 32)),
	}
	got, err := msgIDFor(body)
	if err != nil {
		t.Fatalf("msgIDFor: %v", err)
	}
	const want = "5f229a41d3f68199e00c3afac92455957785affe6c5cd997ed4520626bfbf264"
	if got != want {
		t.Fatalf("canonical-bytes layout drifted on the Go side:\n  got  %s\n  want %s", got, want)
	}
}

func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestMsgIDMatchesCanonicalShape(t *testing.T) {
	// Spot-check: msgIDFor must be a hex sha256 of the canonical bytes
	// described in its doc comment. We reconstruct the bytes here by hand
	// and compare.
	author := pk32(0xAB)
	pk, _ := hex.DecodeString(author)
	body := MessageBody{
		SchemaVersion:      1,
		TopicName:          "build-tasks",
		Seq:                42,
		Body:               "hello",
		PostedAt:           1_700_000_000_123,
		AuthorPuddlePubkey: author,
	}
	got, err := msgIDFor(body)
	if err != nil {
		t.Fatalf("msgIDFor: %v", err)
	}
	// Build canonical bytes the same way the function does.
	var buf []byte
	buf = append(buf, []byte("poolq-msg-v1\x00")...)
	buf = append(buf, beU32(1)...)
	buf = append(buf, pk...)
	buf = append(buf, beU16(uint16(len("build-tasks")))...)
	buf = append(buf, []byte("build-tasks")...)
	buf = append(buf, beU64(42)...)
	buf = append(buf, beI64(1_700_000_000_123)...)
	buf = append(buf, beU32(uint32(len("hello")))...)
	buf = append(buf, []byte("hello")...)
	want := sha256.Sum256(buf)
	if got != hex.EncodeToString(want[:]) {
		t.Fatalf("msg_id mismatch:\n  got  %s\n  want %s", got, hex.EncodeToString(want[:]))
	}
}
