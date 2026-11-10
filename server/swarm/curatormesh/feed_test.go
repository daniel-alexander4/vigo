// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package curatormesh

import (
	"fmt"
	"sort"
	"testing"
)

func TestManifestBlobs(t *testing.T) {
	founderA := pk32(0x11)
	founderB := pk32(0x22)
	shaA1 := pk32(0xa1)
	shaA2 := pk32(0xa2)
	shaB1 := pk32(0xb1)

	ea := CatalogEntry{Body: CatalogEntryBody{
		Name:                "vigo-agent",
		FounderPuddlePubkey: founderA,
		Versions: map[string]ArtifactVersion{
			"1.0.0": {Platforms: []Platform{
				{OS: "linux", Arch: "amd64", BlobSha: shaA1, Size: 100},
				{OS: "linux", Arch: "arm64", BlobSha: shaA2, Size: 110},
			}},
			"2.0.0": {Platforms: []Platform{
				// Same bytes as 1.0.0/amd64 — must dedup to a single manifest blob.
				{OS: "linux", Arch: "amd64", BlobSha: shaA1, Size: 100},
				// Skipped: non-64-hex sha.
				{OS: "darwin", Arch: "arm64", BlobSha: "deadbeef", Size: 120},
				// Skipped: zero size.
				{OS: "freebsd", Arch: "amd64", BlobSha: pk32(0xa3), Size: 0},
			}},
		},
		Target: "os.distro=ubuntu",
	}}
	eb := CatalogEntry{Body: CatalogEntryBody{
		Name:                "thing",
		FounderPuddlePubkey: founderB,
		Versions: map[string]ArtifactVersion{
			"0.1.0": {Platforms: []Platform{{OS: "linux", Arch: "amd64", BlobSha: shaB1, Size: 5}}},
		},
		Target: "", // empty ⇒ "*"
	}}
	// Unparseable founder ⇒ whole entry skipped.
	ebad := CatalogEntry{Body: CatalogEntryBody{
		Name: "bad", FounderPuddlePubkey: "not-hex",
		Versions: map[string]ArtifactVersion{"1": {Platforms: []Platform{{OS: "linux", Arch: "amd64", BlobSha: pk32(0xcc), Size: 9}}}},
	}}

	cat := &Catalog{Artifacts: []CatalogEntry{ea, eb, ebad}}
	got := ManifestBlobs(cat)
	sort.Slice(got, func(i, j int) bool { return got[i].SHA256 < got[j].SHA256 })

	aidA, _ := ArtifactID(ea)
	aidB, _ := ArtifactID(eb)
	want := []ManifestBlob{
		{SHA256: shaA1, Label: fmt.Sprintf("curator:%s:1.0.0:linux-amd64", aidA), Target: "os.distro=ubuntu", Size: 100},
		{SHA256: shaA2, Label: fmt.Sprintf("curator:%s:1.0.0:linux-arm64", aidA), Target: "os.distro=ubuntu", Size: 110},
		{SHA256: shaB1, Label: fmt.Sprintf("curator:%s:0.1.0:linux-amd64", aidB), Target: "*", Size: 5},
	}
	sort.Slice(want, func(i, j int) bool { return want[i].SHA256 < want[j].SHA256 })
	if len(got) != len(want) {
		t.Fatalf("got %d blobs, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("blob %d:\n got  %+v\n want %+v", i, got[i], want[i])
		}
	}
}

func TestManifestBlobsNilCatalog(t *testing.T) {
	if got := ManifestBlobs(nil); got != nil {
		t.Errorf("ManifestBlobs(nil) = %+v, want nil", got)
	}
}
