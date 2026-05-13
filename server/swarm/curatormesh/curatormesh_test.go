// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package curatormesh

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
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

// wantID is the reference artifact_id for (founderHex, name).
func wantID(t *testing.T, founderHex, name string) string {
	t.Helper()
	raw, err := hex.DecodeString(founderHex)
	if err != nil {
		t.Fatalf("bad founder hex: %v", err)
	}
	h := sha256.New()
	h.Write(raw)
	h.Write([]byte(name))
	return hex.EncodeToString(h.Sum(nil))
}

// entry builds a minimal CatalogEntry. signature is left as a label so
// tiebreak behaviour is observable in tests.
func entry(founderHex, name string, issuedAt int64, sig string) CatalogEntry {
	return CatalogEntry{
		Body: CatalogEntryBody{
			SchemaVersion:       1,
			Name:                name,
			FounderPuddlePubkey: founderHex,
			SigningPuddlePubkey: founderHex,
			Versions: map[string]ArtifactVersion{
				"1.0.0": {Platforms: []Platform{
					{OS: "linux", Arch: "amd64", BlobSha: "aa", Size: 10},
					{OS: "linux", Arch: "arm64", BlobSha: "bb", Size: 11},
				}},
				"2.0.0": {Platforms: []Platform{
					{OS: "linux", Arch: "amd64", BlobSha: "cc", Size: 20},
				}},
			},
			Tags:       map[string]string{"latest": "2.0.0", "stable": "1.0.0", "dangling": "9.9.9"},
			Recipients: []string{"*"},
			Target:     "*",
			IssuedAt:   issuedAt,
		},
		Signature: sig,
	}
}

func envelope(entries ...CatalogEntry) *curatorTraitEnvelope {
	return &curatorTraitEnvelope{
		UpdatedAt:     1,
		ArtifactCount: len(entries),
		Artifacts:     entries,
	}
}

func TestArtifactID(t *testing.T) {
	f := pk32(0x11)
	got, err := ArtifactID(entry(f, "vigo-agent", 1, "s"))
	if err != nil {
		t.Fatalf("ArtifactID: %v", err)
	}
	if got != wantID(t, f, "vigo-agent") {
		t.Fatalf("artifact_id mismatch: got %s", got)
	}

	if _, err := ArtifactID(entry("not-hex", "x", 1, "s")); err == nil {
		t.Fatal("expected error on non-hex founder pubkey")
	}
	if _, err := ArtifactID(entry(hex.EncodeToString([]byte{1, 2, 3}), "x", 1, "s")); err == nil {
		t.Fatal("expected error on short founder pubkey")
	}
}

func TestAggregateDedupLWW(t *testing.T) {
	f := pk32(0x22)
	older := entry(f, "tool", 100, "old")
	newer := entry(f, "tool", 200, "new")
	other := entry(pk32(0x33), "other", 50, "x")

	// Two envoys report the same artifact at different issued_at; the newer
	// one wins regardless of which envoy is processed first.
	cat := aggregate([]envelopeRow{
		{hostname: "b-host", envelope: envelope(newer)},
		{hostname: "a-host", envelope: envelope(older, other)},
	})

	if len(cat.Artifacts) != 2 {
		t.Fatalf("want 2 artifacts, got %d", len(cat.Artifacts))
	}
	got := cat.LookupByName(f, "tool")
	if got == nil {
		t.Fatal("tool not found")
	}
	if got.Body.IssuedAt != 200 || got.Signature != "new" {
		t.Fatalf("LWW failed: got issued_at=%d sig=%s", got.Body.IssuedAt, got.Signature)
	}
	// Participants are the contributing hostnames, sorted.
	if len(cat.Participants) != 2 || cat.Participants[0] != "a-host" || cat.Participants[1] != "b-host" {
		t.Fatalf("participants: %v", cat.Participants)
	}
	// Artifacts sorted by artifact_id.
	if cat.Artifacts[0].Body.FounderPuddlePubkey == cat.Artifacts[1].Body.FounderPuddlePubkey {
		t.Fatal("unexpected: both artifacts have the same founder")
	}
	a0, _ := ArtifactID(cat.Artifacts[0])
	a1, _ := ArtifactID(cat.Artifacts[1])
	if a0 >= a1 {
		t.Fatalf("artifacts not sorted by id: %s >= %s", a0, a1)
	}
}

func TestAggregateTiebreakBySignature(t *testing.T) {
	f := pk32(0x44)
	// Same issued_at → higher signature hex wins, deterministically.
	cat := aggregate([]envelopeRow{
		{hostname: "h1", envelope: envelope(entry(f, "t", 100, "aaa"))},
		{hostname: "h2", envelope: envelope(entry(f, "t", 100, "zzz"))},
	})
	got := cat.LookupByName(f, "t")
	if got == nil || got.Signature != "zzz" {
		t.Fatalf("tiebreak: got %+v", got)
	}
	// Reversed input order — same winner.
	cat = aggregate([]envelopeRow{
		{hostname: "h2", envelope: envelope(entry(f, "t", 100, "zzz"))},
		{hostname: "h1", envelope: envelope(entry(f, "t", 100, "aaa"))},
	})
	if got := cat.LookupByName(f, "t"); got == nil || got.Signature != "zzz" {
		t.Fatalf("tiebreak not order-independent: got %+v", got)
	}
}

func TestAggregateDropsMalformedFounder(t *testing.T) {
	cat := aggregate([]envelopeRow{
		{hostname: "h1", envelope: envelope(
			entry("zzzz", "bad", 1, "s"),
			entry(pk32(0x55), "good", 1, "s"),
		)},
	})
	if len(cat.Artifacts) != 1 || cat.Artifacts[0].Body.Name != "good" {
		t.Fatalf("malformed founder not dropped: %+v", cat.Artifacts)
	}
	// Hostname still counts as a participant even though one of its entries was dropped.
	if len(cat.Participants) != 1 || cat.Participants[0] != "h1" {
		t.Fatalf("participants: %v", cat.Participants)
	}
}

func TestLookupByID(t *testing.T) {
	f := pk32(0x66)
	cat := aggregate([]envelopeRow{{hostname: "h", envelope: envelope(entry(f, "n", 1, "s"))}})
	id := wantID(t, f, "n")
	if cat.LookupByID(id) == nil {
		t.Fatal("LookupByID miss")
	}
	// Case-insensitive on the id.
	if cat.LookupByID(strings.ToUpper(id)) == nil {
		t.Fatal("LookupByID should be case-insensitive")
	}
	if cat.LookupByID("deadbeef") != nil {
		t.Fatal("LookupByID should miss unknown id")
	}
	var nilCat *Catalog
	if nilCat.LookupByID(id) != nil {
		t.Fatal("nil catalog lookup must be nil-safe")
	}
}

func TestResolveVersion(t *testing.T) {
	e := entry(pk32(0x77), "n", 1, "s")
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"latest", "2.0.0", true}, // tag
		{"stable", "1.0.0", true}, // tag
		{"1.0.0", "1.0.0", true},  // literal version
		{"2.0.0", "2.0.0", true},  // literal version
		{"dangling", "", false},   // tag pointing at a non-existent version
		{"nope", "", false},       // unknown
		{"", "", false},           // empty
	}
	for _, c := range cases {
		got, ok := ResolveVersion(&e, c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ResolveVersion(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
	if _, ok := ResolveVersion(nil, "latest"); ok {
		t.Error("ResolveVersion(nil) must not resolve")
	}
}

func TestResolvePlatform(t *testing.T) {
	e := entry(pk32(0x88), "n", 1, "s")
	if p := ResolvePlatform(&e, "1.0.0", "linux", "arm64"); p == nil || p.BlobSha != "bb" {
		t.Fatalf("ResolvePlatform 1.0.0/linux/arm64: %+v", p)
	}
	if p := ResolvePlatform(&e, "2.0.0", "linux", "amd64"); p == nil || p.Size != 20 {
		t.Fatalf("ResolvePlatform 2.0.0/linux/amd64: %+v", p)
	}
	if ResolvePlatform(&e, "2.0.0", "linux", "arm64") != nil {
		t.Error("ResolvePlatform should miss absent {os,arch}")
	}
	if ResolvePlatform(&e, "9.9.9", "linux", "amd64") != nil {
		t.Error("ResolvePlatform should miss absent version")
	}
	if ResolvePlatform(nil, "1.0.0", "linux", "amd64") != nil {
		t.Error("ResolvePlatform(nil) must be nil-safe")
	}
}

func TestIsRecipient(t *testing.T) {
	wild := entry(pk32(0x99), "n", 1, "s") // recipients: ["*"]
	if !IsRecipient(&wild, nil) {
		t.Error("`*` recipient must allow everyone")
	}

	scoped := entry(pk32(0xaa), "n", 1, "s")
	scoped.Body.Recipients = []string{pk32(0xbb), pk32(0xcc)}
	if !IsRecipient(&scoped, []string{pk32(0xcc)}) {
		t.Error("listed pubkey must be allowed")
	}
	if !IsRecipient(&scoped, []string{strings.ToUpper(pk32(0xbb))}) {
		t.Error("recipient match must be case-insensitive")
	}
	if IsRecipient(&scoped, []string{pk32(0xff)}) {
		t.Error("unlisted pubkey must be denied")
	}
	if IsRecipient(&scoped, nil) {
		t.Error("no pubkeys → denied for a scoped artifact")
	}

	empty := entry(pk32(0xdd), "n", 1, "s")
	empty.Body.Recipients = nil
	if IsRecipient(&empty, []string{pk32(0xee)}) {
		t.Error("empty recipients must fail closed")
	}
	if IsRecipient(nil, []string{pk32(0xee)}) {
		t.Error("IsRecipient(nil) must be nil-safe")
	}
}
