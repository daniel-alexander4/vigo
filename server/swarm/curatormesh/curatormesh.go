// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package curatormesh builds the fleet-wide curator artifact catalog by
// aggregating every envoy's `curator` trait payload — the signed CatalogEntries
// published by local users — keyed on `artifact_id = sha256(founder_puddle_pubkey ‖ name)`,
// last-write-wins on `issued_at` (ADR-024).
//
// The server publishes, never arbitrates: catalog entries flow through this
// package verbatim. It does NOT verify signatures or delegation chains — the
// agent resolver and `vigocli swarm curator inspect` do that end-to-end (so a
// forged entry never gets *materialized*, even if a misbehaving envoy reports
// it). That's the same stance `gitbackmesh` takes for puddle name claims, and it
// keeps this package free of a Go-side Ed25519 / canonical-bytes twin of
// `agent/src/swarm/curator/catalog.rs`. The only crypto here is the SHA-256 that
// derives the artifact_id (a plain hash, not a signature).
//
// Resolution helpers — tag → version → platform, and the recipient-gate
// predicate — live here so the REST handlers (ADR-024 slice 3) and the CLI views
// (slice 4) call them rather than re-implement them.
//
// Lives in its own package so both `server/api` (REST) and `ui` (web) can
// consume it without an import cycle through `server/fleet` — same as
// `gitbackmesh` / `lockboxmesh` / `longdrawermesh`.
package curatormesh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/db"
)

// EnvoyInput is the minimal envoy descriptor the aggregator needs from its
// caller — (ID, Hostname). Accepting these instead of importing `server/fleet`
// keeps this package at the bottom of the import graph.
type EnvoyInput struct {
	ID       string
	Hostname string
	Revoked  bool
}

// Catalog is the fleet-wide aggregated artifact catalog: the last-write-wins
// winner per artifact_id, plus the hostnames that contributed.
type Catalog struct {
	GeneratedAt  time.Time      `json:"generated_at"`
	Artifacts    []CatalogEntry `json:"artifacts"` // sorted by artifact_id
	Participants []string       `json:"participants"`

	byID map[string]CatalogEntry // artifact_id (hex) → winning entry
}

// --- catalog-entry shapes, deserialized from the agent's `curator` trait.
// Field names mirror agent/src/swarm/curator/catalog.rs's serde output exactly.
// `signature` and `delegations` flow through verbatim — the server doesn't
// verify them.

type CatalogEntry struct {
	Body      CatalogEntryBody `json:"body"`
	Signature string           `json:"signature"`
}

type CatalogEntryBody struct {
	SchemaVersion       uint32                     `json:"schema_version"`
	Name                string                     `json:"name"`
	FounderPuddlePubkey string                     `json:"founder_puddle_pubkey"`
	SigningPuddlePubkey string                     `json:"signing_puddle_pubkey"`
	Versions            map[string]ArtifactVersion `json:"versions"`
	Tags                map[string]string          `json:"tags"`
	Recipients          []string                   `json:"recipients"`
	Target              string                     `json:"target"`
	IssuedAt            int64                      `json:"issued_at"`
	Delegations         []json.RawMessage          `json:"delegations,omitempty"`
}

type ArtifactVersion struct {
	Platforms []Platform `json:"platforms"`
}

type Platform struct {
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	BlobSha string `json:"blob_sha"`
	Size    uint64 `json:"size"`
}

// ArtifactID returns the hex `sha256(founder_puddle_pubkey ‖ name)` for an
// entry — the same derivation as `catalog::artifact_id_for` in the agent.
// Errors when `founder_puddle_pubkey` isn't 32 bytes of hex.
func ArtifactID(e CatalogEntry) (string, error) {
	pk, err := hex.DecodeString(e.Body.FounderPuddlePubkey)
	if err != nil {
		return "", err
	}
	if len(pk) != 32 {
		return "", errors.New("founder_puddle_pubkey is not 32 bytes")
	}
	h := sha256.New()
	h.Write(pk)
	h.Write([]byte(e.Body.Name))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// LookupByID returns the catalog entry for an artifact_id (hex), or nil.
func (c *Catalog) LookupByID(artifactID string) *CatalogEntry {
	if c == nil {
		return nil
	}
	if e, ok := c.byID[strings.ToLower(artifactID)]; ok {
		return &e
	}
	return nil
}

// LookupByName returns the catalog entry for `sha256(founderPubkeyHex ‖ name)`,
// or nil. (`founderPubkeyHex` is the puddle pubkey the name resolves to via the
// ADR-022 fleet name map; the caller does that resolution.)
func (c *Catalog) LookupByName(founderPubkeyHex, name string) *CatalogEntry {
	id, err := ArtifactID(CatalogEntry{Body: CatalogEntryBody{FounderPuddlePubkey: founderPubkeyHex, Name: name}})
	if err != nil {
		return nil
	}
	return c.LookupByID(id)
}

// ResolveVersion maps a tag or a version string to a version string present in
// the entry. A tag takes precedence; a literal version is accepted as-is if it
// exists. Returns ("", false) if neither resolves.
func ResolveVersion(e *CatalogEntry, tagOrVersion string) (string, bool) {
	if e == nil {
		return "", false
	}
	if v, ok := e.Body.Tags[tagOrVersion]; ok {
		if _, present := e.Body.Versions[v]; present {
			return v, true
		}
	}
	if _, present := e.Body.Versions[tagOrVersion]; present {
		return tagOrVersion, true
	}
	return "", false
}

// ResolvePlatform returns the `{os, arch}` platform entry for a version, or nil.
func ResolvePlatform(e *CatalogEntry, version, os, arch string) *Platform {
	if e == nil {
		return nil
	}
	av, ok := e.Body.Versions[version]
	if !ok {
		return nil
	}
	for i := range av.Platforms {
		if av.Platforms[i].OS == os && av.Platforms[i].Arch == arch {
			return &av.Platforms[i]
		}
	}
	return nil
}

// IsRecipient reports whether an envoy hosting users with the given puddle
// pubkeys (hex) may materialize this artifact: `"*"` in `recipients` ⇒ yes
// (the default — fleet-readable); otherwise yes iff any of the envoy's puddle
// pubkeys is listed. A missing/empty `recipients` list ⇒ no (fail closed).
func IsRecipient(e *CatalogEntry, envoyPuddlePubkeys []string) bool {
	if e == nil {
		return false
	}
	allowed := make(map[string]struct{}, len(e.Body.Recipients))
	for _, r := range e.Body.Recipients {
		if r == "*" {
			return true
		}
		allowed[strings.ToLower(r)] = struct{}{}
	}
	for _, pk := range envoyPuddlePubkeys {
		if _, ok := allowed[strings.ToLower(pk)]; ok {
			return true
		}
	}
	return false
}

// Aggregator builds the fleet catalog from the trait pipeline. One per server
// (or per consuming handler); cheap to construct.
type Aggregator struct {
	queries *db.Queries
}

// NewAggregator constructs an aggregator over the given query layer.
func NewAggregator(queries *db.Queries) *Aggregator {
	return &Aggregator{queries: queries}
}

// Snapshot builds the fleet catalog from every (non-revoked) envoy's `curator`
// trait.
func (a *Aggregator) Snapshot(ctx context.Context, envoys []EnvoyInput) (*Catalog, error) {
	rows := make([]envelopeRow, 0, len(envoys))
	for _, e := range envoys {
		if e.Revoked {
			continue
		}
		env, ok := a.loadTrait(ctx, e.ID)
		if !ok {
			continue
		}
		rows = append(rows, envelopeRow{hostname: e.Hostname, envelope: env})
	}
	cat := aggregate(rows)
	cat.GeneratedAt = time.Now().UTC()
	return cat, nil
}

type envelopeRow struct {
	hostname string
	envelope *curatorTraitEnvelope
}

// aggregate is the pure core: collapse every reported entry into the
// last-write-wins winner per artifact_id (tiebreak: higher `signature` hex —
// arbitrary but deterministic; same-id entries are necessarily from the same
// founder, so a tie is two publishes at the same millisecond), and collect the
// contributing hostnames. Entries with an unparseable `founder_puddle_pubkey`
// are dropped — same as a missing trait.
func aggregate(rows []envelopeRow) *Catalog {
	byID := make(map[string]CatalogEntry)
	participants := make(map[string]struct{})
	for _, row := range rows {
		participants[row.hostname] = struct{}{}
		for _, e := range row.envelope.Artifacts {
			id, err := ArtifactID(e)
			if err != nil {
				continue
			}
			cur, ok := byID[id]
			if !ok || newer(e, cur) {
				byID[id] = e
			}
		}
	}

	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	artifacts := make([]CatalogEntry, 0, len(byID))
	for _, id := range ids {
		artifacts = append(artifacts, byID[id])
	}

	partList := make([]string, 0, len(participants))
	for h := range participants {
		partList = append(partList, h)
	}
	sort.Strings(partList)

	return &Catalog{
		Artifacts:    artifacts,
		Participants: partList,
		byID:         byID,
	}
}

// newer reports whether candidate `c` should win over the incumbent `i` —
// last-write-wins on `issued_at`, tiebroken by the (deterministic) signature
// hex so the merge is order-independent.
func newer(c, i CatalogEntry) bool {
	if c.Body.IssuedAt != i.Body.IssuedAt {
		return c.Body.IssuedAt > i.Body.IssuedAt
	}
	return c.Signature > i.Signature
}

// loadTrait pulls the latest traits row for an envoy and extracts the `curator`
// namespace. Missing rows, malformed JSON, and a missing `curator` namespace all
// collapse to `(nil, false)` — treated the same as "no participation reported".
func (a *Aggregator) loadTrait(ctx context.Context, envoyID string) (*curatorTraitEnvelope, bool) {
	tr, err := a.queries.GetLatestEnvoyTraits(ctx, envoyID)
	if err != nil {
		return nil, false
	}
	var env struct {
		Curator *curatorTraitEnvelope `json:"curator"`
	}
	if err := json.Unmarshal([]byte(tr.Traits), &env); err != nil {
		return nil, false
	}
	if env.Curator == nil {
		return nil, false
	}
	return env.Curator, true
}

// curatorTraitEnvelope is the top-level shape agent/src/traits/curator.rs emits.
type curatorTraitEnvelope struct {
	UpdatedAt     int64          `json:"updated_at"`
	ArtifactCount int            `json:"artifact_count"`
	Artifacts     []CatalogEntry `json:"artifacts"`
}
