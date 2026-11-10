// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package curatormesh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Server-side twin of agent/src/swarm/curator/catalog.rs `CatalogEntry::verify`
// + `canonical_bytes`, and of agent/src/swarm/anchor/envelopes.rs
// `canonical_delegation_bytes` / `verify_signature`. Byte-for-byte: the
// cross-language vectors in verify_test.go are produced by the Rust signer
// (catalog.rs `go_verifier_vectors_are_stable`). Change both sides together.
//
// Why the server verifies at all: the aggregate winner is served verbatim to
// non-verifying consumers (curators3's S3 facade, the UI, the manifest feed),
// and its last-write-wins key `issued_at` is chosen by the reporter. An
// unverified entry let any enrolled envoy point `<publisher>/<artifact>` at
// any cached blob or widen its recipients.

// catalogDomain / delegationDomain are the agent's signing domains, byte-exact.
const (
	catalogDomain    = "curator-catalog-v2\x00"
	delegationDomain = "gitback-delegate-v2\x00"
)

// delegationMaxLen mirrors anchor::envelopes::DELEGATION_MAX_LEN (ADR-019).
const delegationMaxLen = 5

// delegationEnvelope mirrors anchor::envelopes::DelegationEnvelope's serde shape.
type delegationEnvelope struct {
	ProjectID        string `json:"project_id"`
	OldFounderPubkey string `json:"old_founder_pubkey"`
	NewFounderPubkey string `json:"new_founder_pubkey"`
	DelegatedAt      int64  `json:"delegated_at"`
	Signature        string `json:"signature"`
}

// parseHex32 mirrors catalog.rs parse_hex32: any-case hex, exactly 32 bytes.
func parseHex32(s, what string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid hex", what)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("%s: %d bytes, expected 32", what, len(b))
	}
	return b, nil
}

// parseIDHex mirrors envelopes.rs parse_id_hex: exactly 64 LOWERCASE hex chars.
func parseIDHex(s string) ([]byte, error) {
	if len(s) != 64 {
		return nil, fmt.Errorf("expected 64-char hex id, got %d chars", len(s))
	}
	if strings.ToLower(s) != s {
		return nil, errors.New("id must be lowercase hex")
	}
	return hex.DecodeString(s)
}

func putStr(out *bytes.Buffer, s, what string) error {
	if len(s) > 0xFFFF {
		return fmt.Errorf("%s too long for canonical encoding (%d bytes)", what, len(s))
	}
	_ = binary.Write(out, binary.BigEndian, uint16(len(s)))
	out.WriteString(s)
	return nil
}

func putCount(out *bytes.Buffer, n int, what string) error {
	if n > 0xFFFF {
		return fmt.Errorf("%s count %d exceeds canonical-encoding limit", what, n)
	}
	_ = binary.Write(out, binary.BigEndian, uint16(n))
	return nil
}

// decodeDelegations parses the verbatim delegation JSON.
func decodeDelegations(raw []json.RawMessage) ([]delegationEnvelope, error) {
	out := make([]delegationEnvelope, 0, len(raw))
	for i, r := range raw {
		var d delegationEnvelope
		if err := json.Unmarshal(r, &d); err != nil {
			return nil, fmt.Errorf("delegation #%d: %w", i, err)
		}
		out = append(out, d)
	}
	return out, nil
}

// delegationRaw is a decoded delegation (envelopes.rs RawDelegation).
type delegationRaw struct {
	projectID, oldFounder, newFounder, signature []byte
	delegatedAt                                  int64
}

func (d delegationEnvelope) raw() (delegationRaw, error) {
	pid, err := parseIDHex(d.ProjectID)
	if err != nil {
		return delegationRaw{}, fmt.Errorf("project_id hex: %w", err)
	}
	oldPK, err := parseIDHex(d.OldFounderPubkey)
	if err != nil {
		return delegationRaw{}, fmt.Errorf("old_founder_pubkey hex: %w", err)
	}
	newPK, err := parseIDHex(d.NewFounderPubkey)
	if err != nil {
		return delegationRaw{}, fmt.Errorf("new_founder_pubkey hex: %w", err)
	}
	sig, err := hex.DecodeString(d.Signature)
	if err != nil {
		return delegationRaw{}, errors.New("signature hex")
	}
	if len(sig) != ed25519.SignatureSize {
		return delegationRaw{}, fmt.Errorf("signature is %d bytes, expected %d", len(sig), ed25519.SignatureSize)
	}
	return delegationRaw{projectID: pid, oldFounder: oldPK, newFounder: newPK, signature: sig, delegatedAt: d.DelegatedAt}, nil
}

// canonicalDelegationBytes mirrors envelopes.rs canonical_delegation_bytes.
func canonicalDelegationBytes(r delegationRaw) []byte {
	var out bytes.Buffer
	out.WriteString(delegationDomain)
	out.Write(r.projectID)
	out.Write(r.oldFounder)
	out.Write(r.newFounder)
	_ = binary.Write(&out, binary.BigEndian, r.delegatedAt)
	return out.Bytes()
}

// sortedKeys returns m's keys in byte-wise order — Rust BTreeMap<String,_> order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// canonicalBytes mirrors catalog.rs canonical_bytes exactly (layout documented
// there).
func canonicalBytes(b *CatalogEntryBody, dels []delegationEnvelope) ([]byte, error) {
	founder, err := parseHex32(b.FounderPuddlePubkey, "founder_puddle_pubkey")
	if err != nil {
		return nil, err
	}
	signer, err := parseHex32(b.SigningPuddlePubkey, "signing_puddle_pubkey")
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString(catalogDomain)
	_ = binary.Write(&out, binary.BigEndian, b.SchemaVersion)
	out.Write(founder)
	out.Write(signer)
	if err := putStr(&out, b.Name, "name"); err != nil {
		return nil, err
	}
	_ = binary.Write(&out, binary.BigEndian, b.IssuedAt)
	if err := putStr(&out, b.Target, "target"); err != nil {
		return nil, err
	}

	if err := putCount(&out, len(b.Recipients), "recipients"); err != nil {
		return nil, err
	}
	for _, r := range b.Recipients {
		if err := putStr(&out, r, "recipient"); err != nil {
			return nil, err
		}
	}

	if err := putCount(&out, len(b.Versions), "versions"); err != nil {
		return nil, err
	}
	for _, ver := range sortedKeys(b.Versions) {
		av := b.Versions[ver]
		if err := putStr(&out, ver, "version string"); err != nil {
			return nil, err
		}
		if err := putCount(&out, len(av.Platforms), "platforms"); err != nil {
			return nil, err
		}
		for _, p := range av.Platforms {
			if err := putStr(&out, p.OS, "platform os"); err != nil {
				return nil, err
			}
			if err := putStr(&out, p.Arch, "platform arch"); err != nil {
				return nil, err
			}
			sha, err := parseHex32(p.BlobSha, "platform blob_sha")
			if err != nil {
				return nil, err
			}
			out.Write(sha)
			_ = binary.Write(&out, binary.BigEndian, p.Size)
		}
	}

	if err := putCount(&out, len(b.Tags), "tags"); err != nil {
		return nil, err
	}
	for _, tag := range sortedKeys(b.Tags) {
		if err := putStr(&out, tag, "tag"); err != nil {
			return nil, err
		}
		if err := putStr(&out, b.Tags[tag], "tag target version"); err != nil {
			return nil, err
		}
	}

	if err := putCount(&out, len(dels), "delegations"); err != nil {
		return nil, err
	}
	for i, d := range dels {
		r, err := d.raw()
		if err != nil {
			return nil, fmt.Errorf("delegation envelope #%d: %w", i, err)
		}
		out.Write(r.projectID)
		out.Write(r.oldFounder)
		out.Write(r.newFounder)
		_ = binary.Write(&out, binary.BigEndian, r.delegatedAt)
		out.Write(r.signature)
	}

	if err := putStr(&out, b.Kind, "kind"); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// VerifyEntry is the full anchor-rooted check catalog.rs CatalogEntry::verify
// performs: (1) the entry signature verifies under signing_puddle_pubkey over
// canonicalBytes(body); (2) with no delegation chain, signing == founder;
// with one, it is ≤ delegationMaxLen links, each link's own signature
// verifies, each is for this artifact, the links bridge founder → … →
// signing, and delegated_at strictly increases.
func VerifyEntry(e *CatalogEntry) error {
	dels, err := decodeDelegations(e.Body.Delegations)
	if err != nil {
		return err
	}
	signer, err := parseHex32(e.Body.SigningPuddlePubkey, "signing_puddle_pubkey")
	if err != nil {
		return err
	}
	sig, err := hex.DecodeString(e.Signature)
	if err != nil {
		return errors.New("catalog entry signature hex")
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("catalog entry signature is %d bytes, expected %d", len(sig), ed25519.SignatureSize)
	}
	msg, err := canonicalBytes(&e.Body, dels)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(signer), msg, sig) {
		return errors.New("catalog entry signature verification failed")
	}

	founder, err := parseHex32(e.Body.FounderPuddlePubkey, "founder_puddle_pubkey")
	if err != nil {
		return err
	}
	h := sha256.New()
	h.Write(founder)
	h.Write([]byte(e.Body.Name))
	artifactID := h.Sum(nil)

	if len(dels) == 0 {
		if !bytes.Equal(signer, founder) {
			return errors.New("catalog entry has no delegation chain but the signing pubkey ≠ the founder pubkey it's anchored to")
		}
		return nil
	}
	if len(dels) > delegationMaxLen {
		return fmt.Errorf("delegation chain is %d hops, over the %d-hop cap", len(dels), delegationMaxLen)
	}
	prevNew := founder
	var prevAt int64
	for i, d := range dels {
		r, err := d.raw()
		if err != nil {
			return fmt.Errorf("delegation #%d: %w", i, err)
		}
		if !ed25519.Verify(ed25519.PublicKey(r.oldFounder), canonicalDelegationBytes(r), r.signature) {
			return fmt.Errorf("delegation #%d signature verification failed", i)
		}
		if !bytes.Equal(r.projectID, artifactID) {
			return fmt.Errorf("delegation #%d is for a different artifact (project_id mismatch)", i)
		}
		if !bytes.Equal(r.oldFounder, prevNew) {
			return fmt.Errorf("delegation chain broken at #%d: doesn't continue from the previous link", i)
		}
		if i > 0 && d.DelegatedAt <= prevAt {
			return fmt.Errorf("delegation #%d is not strictly later than #%d", i, i-1)
		}
		prevAt = d.DelegatedAt
		prevNew = r.newFounder
	}
	if !bytes.Equal(prevNew, signer) {
		return errors.New("delegation chain doesn't terminate at the entry's signing pubkey")
	}
	return nil
}
