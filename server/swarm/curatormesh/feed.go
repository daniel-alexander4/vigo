// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package curatormesh

import (
	"fmt"
	"sort"
)

// ManifestBlob is one `{artifact, version, os, arch}` build's swarm-blob
// coordinates, derived from a catalog entry — the unit the server feeds into its
// authoritative `swarm.ManifestStore` so the existing filecast distribution
// machinery carries curator artifact bytes fleet-wide (ADR-024 slice 3).
type ManifestBlob struct {
	SHA256 string // content address of the blob
	Label  string // `curator:<artifact_id_hex>:<version>:<os>-<arch>` — distinct from gitback's `gitback:*` claim-only namespace
	Target string // the artifact's `target` glob — which envoys hold the P2P replica (default "*")
	Size   uint64
}

// ManifestBlobs walks a fleet catalog and returns every distinct
// `{artifact, version, platform}` blob as a [`ManifestBlob`]. Entries whose
// `founder_puddle_pubkey` is unparseable (so `ArtifactID` fails) and platforms
// with a non-64-hex `blob_sha` are skipped — the server only feeds blobs the
// content-address machinery can actually move. Dedups on `blob_sha` (two
// `{version, platform}` slots pointing at the same bytes ⇒ one manifest entry,
// keyed by the first one seen — `target` is per-artifact so a collision implies
// the same target).
func ManifestBlobs(cat *Catalog) []ManifestBlob {
	if cat == nil {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]ManifestBlob, 0)
	for _, e := range cat.Artifacts {
		aid, err := ArtifactID(e)
		if err != nil {
			continue
		}
		target := e.Body.Target
		if target == "" {
			target = "*"
		}
		versions := make([]string, 0, len(e.Body.Versions))
		for v := range e.Body.Versions {
			versions = append(versions, v)
		}
		sort.Strings(versions) // deterministic output (and a stable dedup winner)
		for _, version := range versions {
			av := e.Body.Versions[version]
			for _, p := range av.Platforms {
				if !isHex64(p.BlobSha) || p.Size == 0 {
					continue
				}
				sha := lowerHex(p.BlobSha)
				if _, dup := seen[sha]; dup {
					continue
				}
				seen[sha] = struct{}{}
				out = append(out, ManifestBlob{
					SHA256: sha,
					Label:  fmt.Sprintf("curator:%s:%s:%s-%s", aid, version, p.OS, p.Arch),
					Target: target,
					Size:   p.Size,
				})
			}
		}
	}
	return out
}

// ManifestLabelPrefix is the `swarm.ManifestStore` label namespace curator
// entries live under; the feed reconcile revokes `curator:*` entries no longer
// present in the catalog.
const ManifestLabelPrefix = "curator:"

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func lowerHex(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'F' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
