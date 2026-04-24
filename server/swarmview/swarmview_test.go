// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarmview

import (
	"testing"
	"time"

	"git.alexander4.io/vigo/server/swarm"
)

// TestSnapshotStatusSplitsByLabelPrefix pins the "cairn:" label
// prefix → Cairn bucket, everything else → Lode bucket rule used
// by the substrate status rollup. The rule lives implicitly in the
// seed-label naming convention, so this test guards against a seed
// prefix rename silently miscounting payloads on the /swarm page.
func TestSnapshotStatusSplitsByLabelPrefix(t *testing.T) {
	tracker := swarm.NewPeerTracker(5 * time.Minute)
	tracker.RegisterSeed("aaaa", swarm.SeedState{Label: "app-v2.tar.gz", SeederID: "envoy-1", Size: 100})
	tracker.RegisterSeed("bbbb", swarm.SeedState{Label: "cairn:danlap:dan:web", SeederID: "envoy-2", Size: 50})
	tracker.RegisterSeed("cccc", swarm.SeedState{Label: "firmware.bin", SeederID: "envoy-1", Size: 200})

	src := Sources{
		Tracker: tracker,
		FleetSource: func() []EnvoyInput {
			return []EnvoyInput{
				{ID: "envoy-1", Hostname: "danlap", LastSeen: time.Now()},
				{ID: "envoy-2", Hostname: "homeserver", Offline: true},
			}
		},
	}

	s := SnapshotStatus(src)
	if s.Payloads.Lode != 2 {
		t.Errorf("expected 2 lode payloads (app-v2 + firmware), got %d", s.Payloads.Lode)
	}
	if s.Payloads.Cairn != 1 {
		t.Errorf("expected 1 cairn payload, got %d", s.Payloads.Cairn)
	}
	// Both seeders are distinct envoys.
	if s.EnvoysSeeding != 2 {
		t.Errorf("expected 2 seeding envoys, got %d", s.EnvoysSeeding)
	}
	// Without ForEachPeer reports, substrate-active = just the two seeders.
	if s.EnvoysSubstrateActive != 2 {
		t.Errorf("expected 2 substrate-active envoys (seeders), got %d", s.EnvoysSubstrateActive)
	}
	if s.EnvoysTotal != 2 {
		t.Errorf("expected 2 envoys total, got %d", s.EnvoysTotal)
	}
	if s.EnvoysOnline != 1 {
		t.Errorf("expected 1 online envoy (homeserver is offline), got %d", s.EnvoysOnline)
	}
}

// TestSnapshotPeersSortsAndLimits covers the sort + limit plumbing so
// the CLI / UI behavior is pinned even if the underlying rollup
// rules shift.
func TestSnapshotPeersSortsAndLimits(t *testing.T) {
	tracker := swarm.NewPeerTracker(5 * time.Minute)
	tracker.RegisterSeed("aa", swarm.SeedState{Label: "app.tar.gz", SeederID: "e1", Size: 1000})

	src := Sources{
		Tracker: tracker,
		FleetSource: func() []EnvoyInput {
			return []EnvoyInput{
				{ID: "e1", Hostname: "alpha"},
				{ID: "e2", Hostname: "beta"},
				{ID: "e3", Hostname: "gamma"},
			}
		},
	}

	// Hostname sort is alphabetical; limit 2 trims to alpha + beta.
	byHost := SnapshotPeers(src, "hostname", 2)
	if byHost.Total != 3 {
		t.Errorf("expected Total=3, got %d", byHost.Total)
	}
	if byHost.Returned != 2 {
		t.Errorf("expected Returned=2 after limit, got %d", byHost.Returned)
	}
	if len(byHost.Envoys) != 2 || byHost.Envoys[0].Hostname != "alpha" {
		t.Errorf("expected alpha first, got %+v", byHost.Envoys)
	}

	// Limit 0 = unbounded.
	all := SnapshotPeers(src, "hostname", 0)
	if all.Returned != 3 {
		t.Errorf("limit=0 should be unbounded, got Returned=%d", all.Returned)
	}

	// Seeding count is populated for e1 only.
	for _, row := range all.Envoys {
		if row.Hostname == "alpha" && row.SeedingCount != 1 {
			t.Errorf("alpha should have seeding_count=1, got %d", row.SeedingCount)
		}
		if row.Hostname == "beta" && row.SeedingCount != 0 {
			t.Errorf("beta should have seeding_count=0, got %d", row.SeedingCount)
		}
	}
}

// TestSnapshotStatusRevokedManifestEntries pins the active-vs-revoked
// split on ManifestStore entries. The sentinel value is the string
// "revoked" (matches server/swarm/manifest.go ManifestEntry.Status).
func TestSnapshotStatusRevokedManifestEntries(t *testing.T) {
	tracker := swarm.NewPeerTracker(5 * time.Minute)
	manifest := swarm.NewManifestStore()
	manifest.RecordSeed("aa", "app.tar.gz", "*", "7d", "server", 100)
	manifest.RecordSeed("bb", "old.tar.gz", "*", "7d", "server", 200)
	manifest.Revoke("bb")

	src := Sources{
		Tracker:       tracker,
		ManifestStore: manifest,
		FleetSource:   func() []EnvoyInput { return nil },
	}
	s := SnapshotStatus(src)
	if s.ManifestEntriesActive != 1 {
		t.Errorf("expected 1 active manifest entry, got %d", s.ManifestEntriesActive)
	}
	if s.ManifestEntriesRevoked != 1 {
		t.Errorf("expected 1 revoked manifest entry, got %d", s.ManifestEntriesRevoked)
	}
}
