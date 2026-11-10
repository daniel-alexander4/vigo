// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package curatormesh

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// Produced by the Rust signer: agent/src/swarm/curator/catalog.rs
// `go_verifier_vectors_are_stable` (fixed keys 0x11×32 founder, 0x22×32
// rekeyed). That test asserts the agent still emits these exact strings, so a
// drift on either side goes red.
const (
	rustVectorPlain   = `{"body":{"schema_version":2,"name":"vigo-agent","kind":"generic","founder_puddle_pubkey":"d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737","signing_puddle_pubkey":"d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737","versions":{"0.9.0":{"platforms":[{"os":"linux","arch":"arm64","blob_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":7},{"os":"darwin","arch":"arm64","blob_sha":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":1099511627776}]},"1.0.0":{"platforms":[{"os":"linux","arch":"amd64","blob_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":12345}]}},"tags":{"latest":"1.0.0","stable":"0.9.0"},"recipients":["a09aa5f47a6759802ff955f8dc2d2a14a5c99d23be97f864127ff9383455a4f0","d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737"],"target":"*.web.example.com","issued_at":1762000000123},"signature":"f112eda3dccd85ca23b2930106794c52a36e1093139bce1d75e8c8699a646c5f3c1bd403cfd846c6c6c53e157114959e23ea203552019a678c5f602e15010b0d"}`
	rustVectorChained = `{"body":{"schema_version":2,"name":"app","kind":"generic","founder_puddle_pubkey":"d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737","signing_puddle_pubkey":"a09aa5f47a6759802ff955f8dc2d2a14a5c99d23be97f864127ff9383455a4f0","versions":{"1.0.0":{"platforms":[{"os":"linux","arch":"amd64","blob_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":12345}]}},"tags":{"latest":"1.0.0"},"recipients":["*"],"target":"*","issued_at":1762000999000,"delegations":[{"project_id":"c6d907581c7e0227fee5b6f265d81feab6d61fce9a7d53a184bb135f1479373e","old_founder_pubkey":"d04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737","new_founder_pubkey":"a09aa5f47a6759802ff955f8dc2d2a14a5c99d23be97f864127ff9383455a4f0","delegated_at":1762000500,"signature":"dd8835033074c53238ec10b88c752f37fffddd8eb1293438d228e00f3cf36c7ae64755d88601b707f13b14ab9968a21f88443221e608d46c9f181735ad3e6101"}]},"signature":"dd862cfe3a467704bb5020f9b06268c4cd2eb5967a42713101e0bf8b789c2e71ee23c4e47a3032212ffce5651253d5d0983a5fe192b5e9c07c35757fe92ba709"}`
)

func decodeVector(t *testing.T, s string) CatalogEntry {
	t.Helper()
	var e CatalogEntry
	if err := json.Unmarshal([]byte(s), &e); err != nil {
		t.Fatalf("decode vector: %v", err)
	}
	return e
}

// TestVerifyEntryRustVectors: the Go verifier accepts what the Rust signer
// signs — byte-exact canonical encoding, including the delegation chain — and
// rejects any single-field tamper of it.
func TestVerifyEntryRustVectors(t *testing.T) {
	for name, vec := range map[string]string{"plain": rustVectorPlain, "chained": rustVectorChained} {
		e := decodeVector(t, vec)
		if err := VerifyEntry(&e); err != nil {
			t.Fatalf("%s: Rust-signed entry rejected: %v", name, err)
		}

		tampers := map[string]func(*CatalogEntry){
			"issued_at":  func(e *CatalogEntry) { e.Body.IssuedAt++ },
			"recipients": func(e *CatalogEntry) { e.Body.Recipients = append(e.Body.Recipients, "*") },
			"target":     func(e *CatalogEntry) { e.Body.Target += "x" },
			"kind":       func(e *CatalogEntry) { e.Body.Kind = "oci" },
			"schema":     func(e *CatalogEntry) { e.Body.SchemaVersion++ },
			"name":       func(e *CatalogEntry) { e.Body.Name += "x" },
			"tag": func(e *CatalogEntry) {
				for k := range e.Body.Tags {
					e.Body.Tags[k] = "nope"
				}
			},
			"blob_sha": func(e *CatalogEntry) {
				for v, av := range e.Body.Versions {
					av.Platforms[0].BlobSha = strings.Repeat("d", 64)
					e.Body.Versions[v] = av
				}
			},
			"size": func(e *CatalogEntry) {
				for v, av := range e.Body.Versions {
					av.Platforms[0].Size++
					e.Body.Versions[v] = av
				}
			},
		}
		for tn, mut := range tampers {
			te := decodeVector(t, vec)
			mut(&te)
			if err := VerifyEntry(&te); err == nil {
				t.Errorf("%s: tampered %s accepted", name, tn)
			}
		}
	}

	// The chained vector really exercises the chain: dropping it must fail.
	e := decodeVector(t, rustVectorChained)
	if len(e.Body.Delegations) == 0 {
		t.Fatal("chained vector carries no delegation")
	}
	e.Body.Delegations = nil
	if err := VerifyEntry(&e); err == nil {
		t.Error("chained entry accepted with its delegation chain removed")
	}
}

// delegate builds a Go-signed delegation link old → new for artifactID.
func delegate(t *testing.T, oldKey ed25519.PrivateKey, artifactID, newPub []byte, at int64) json.RawMessage {
	t.Helper()
	r := delegationRaw{
		projectID:   artifactID,
		oldFounder:  oldKey.Public().(ed25519.PublicKey),
		newFounder:  newPub,
		delegatedAt: at,
	}
	sig := ed25519.Sign(oldKey, canonicalDelegationBytes(r))
	raw, err := json.Marshal(delegationEnvelope{
		ProjectID:        hex.EncodeToString(artifactID),
		OldFounderPubkey: hex.EncodeToString(r.oldFounder),
		NewFounderPubkey: hex.EncodeToString(newPub),
		DelegatedAt:      at,
		Signature:        hex.EncodeToString(sig),
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// forgedDelegate is a link that CLAIMS claimedOld as its signer but is
// signed by signerKey — the founder never delegated.
func forgedDelegate(t *testing.T, signerKey ed25519.PrivateKey, claimedOld, artifactID, newPub []byte, at int64) json.RawMessage {
	t.Helper()
	r := delegationRaw{projectID: artifactID, oldFounder: claimedOld, newFounder: newPub, delegatedAt: at}
	sig := ed25519.Sign(signerKey, canonicalDelegationBytes(r))
	raw, err := json.Marshal(delegationEnvelope{
		ProjectID:        hex.EncodeToString(artifactID),
		OldFounderPubkey: hex.EncodeToString(claimedOld),
		NewFounderPubkey: hex.EncodeToString(newPub),
		DelegatedAt:      at,
		Signature:        hex.EncodeToString(sig),
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestVerifyEntryDelegationChain covers each chain rule catalog.rs enforces.
func TestVerifyEntryDelegationChain(t *testing.T) {
	f, k2, k3 := pk32(0x31), pk32(0x32), pk32(0x33)
	fk, kk2, kk3 := testKeys[f], testKeys[k2], testKeys[k3]
	aidHex := wantID(t, f, "app")
	aid, _ := hex.DecodeString(aidHex)
	pub := func(h string) []byte { b, _ := hex.DecodeString(h); return b }

	build := func(signer string, dels ...json.RawMessage) CatalogEntry {
		e := entry(f, "app", 10, "")
		e.Body.SigningPuddlePubkey = signer
		e.Body.Delegations = dels
		return signEntry(e)
	}

	ok := build(k3, delegate(t, fk, aid, pub(k2), 1), delegate(t, kk2, aid, pub(k3), 2))
	if err := VerifyEntry(&ok); err != nil {
		t.Fatalf("valid two-link chain rejected: %v", err)
	}

	otherAID, _ := hex.DecodeString(wantID(t, f, "other"))
	cases := map[string]CatalogEntry{
		"no chain, signer ≠ founder": build(k2),
		"wrong artifact":             build(k2, delegate(t, fk, otherAID, pub(k2), 1)),
		"broken link":                build(k3, delegate(t, fk, aid, pub(k2), 1), delegate(t, kk3, aid, pub(k3), 2)),
		"non-monotonic":              build(k3, delegate(t, fk, aid, pub(k2), 5), delegate(t, kk2, aid, pub(k3), 5)),
		"does not end at signer":     build(k3, delegate(t, fk, aid, pub(k2), 1)),
		"forged link signature":      build(k2, forgedDelegate(t, kk2, pub(f), aid, pub(k2), 1)),
	}
	var long []json.RawMessage
	prev := fk
	for i := 0; i < delegationMaxLen+1; i++ {
		nk := pk32(byte(0x40 + i))
		long = append(long, delegate(t, prev, aid, pub(nk), int64(i+1)))
		prev = testKeys[nk]
	}
	cases["over cap"] = build(hex.EncodeToString(prev.Public().(ed25519.PublicKey)), long...)
	for name, e := range cases {
		if err := VerifyEntry(&e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestAggregateDropsForgedEntry: an enrolled envoy cannot repoint another
// publisher's artifact. A forged entry for founder F's artifact — signed by
// the attacker's own key, or claiming F's key with a bad signature, or a
// valid entry with its blob_sha swapped — at a HIGHER issued_at must not win.
func TestAggregateDropsForgedEntry(t *testing.T) {
	f := pk32(0x51)
	attacker := pk32(0x52)
	legit := entry(f, "tool", 100, "")

	selfSigned := entry(f, "tool", 999, "")
	selfSigned.Body.SigningPuddlePubkey = attacker
	selfSigned = signEntry(selfSigned)

	badSig := entry(f, "tool", 999, "")
	badSig.Signature = strings.Repeat("00", 64)

	swapped := entry(f, "tool", 999, "")
	av := swapped.Body.Versions["2.0.0"]
	av.Platforms[0].BlobSha = strings.Repeat("ee", 32)
	swapped.Body.Versions["2.0.0"] = av

	for name, forged := range map[string]CatalogEntry{"self-signed": selfSigned, "bad signature": badSig, "blob swapped": swapped} {
		cat := aggregate([]envelopeRow{
			{hostname: "victim", envelope: envelope(legit)},
			{hostname: "attacker", envelope: envelope(forged)},
		})
		got := cat.LookupByName(f, "tool")
		if got == nil || got.Body.IssuedAt != 100 || got.Signature != legit.Signature {
			t.Errorf("%s: forged entry won the aggregate: %+v", name, got)
		}
	}
}
