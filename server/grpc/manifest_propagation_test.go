// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/swarm"
)

// TestCollectManifestEntries_PopulatesFromStore verifies the helper that
// serializes the in-memory ManifestStore into the proto messages shipped on
// CheckInResponse.manifest_entries. This is one half of the manifest CRDT
// propagation path fixed in 0.20.7 — the other half (verifying the helper is
// actually called from each CheckInResponse builder) lives in the wiring test
// below.
func TestCollectManifestEntries_PopulatesFromStore(t *testing.T) {
	ms := swarm.NewManifestStore()
	ms.RecordSeed(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"win11.iso", "*.host", "7d", "envoy-1", 7_000_000_000,
	)
	ms.Revoke("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	svc := &AgentService{manifestStore: ms}
	entries := svc.collectManifestEntries()

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Index by sha256 — the iteration order over the map is non-deterministic.
	bySha := make(map[string]int, len(entries))
	for i, e := range entries {
		bySha[e.Sha256] = i
	}

	active := entries[bySha["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]]
	if active.Status != "active" {
		t.Errorf("expected active entry status=active, got %q", active.Status)
	}
	if active.Label != "win11.iso" {
		t.Errorf("expected label=win11.iso, got %q", active.Label)
	}
	if active.SeederId != "envoy-1" {
		t.Errorf("expected seeder_id=envoy-1, got %q", active.SeederId)
	}
	if active.Size != 7_000_000_000 {
		t.Errorf("expected size=7_000_000_000, got %d", active.Size)
	}

	revoked := entries[bySha["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]]
	if revoked.Status != "revoked" {
		t.Errorf("expected revoked entry status=revoked, got %q", revoked.Status)
	}
	if revoked.UpdatedBy != "server" {
		t.Errorf("expected revoked.updated_by=server, got %q", revoked.UpdatedBy)
	}
}

func TestCollectManifestEntries_NilStoreReturnsNil(t *testing.T) {
	svc := &AgentService{manifestStore: nil}
	if got := svc.collectManifestEntries(); got != nil {
		t.Errorf("expected nil for nil store, got %v", got)
	}
}

func TestCollectManifestEntries_EmptyStoreReturnsNil(t *testing.T) {
	svc := &AgentService{manifestStore: swarm.NewManifestStore()}
	if got := svc.collectManifestEntries(); got != nil {
		t.Errorf("expected nil for empty store, got %v", got)
	}
}

// TestBuildCheckInResponse_AllPathsShipManifestEntries is a wiring assertion:
// every `&pb.CheckInResponse{...}` literal in agentstream.go must be paired
// with a `resp.ManifestEntries = s.collectManifestEntries()` assignment before
// the corresponding return statement.
//
// This exists because the original bug was structural: the helper existed,
// the proto field existed, the agent consumed the field, but no
// CheckInResponse builder ever called the helper. A normal unit test of
// `collectManifestEntries` would have caught nothing — the function returned
// the right data, it just wasn't connected to anything.
//
// A runtime integration test exercising each of the three response builders
// would catch the bug too, but each path requires substantial AgentService
// setup (configStore, db, fleetIndex) and three independent test scaffolds.
// This text-level assertion catches the EXACT bug class with zero setup,
// runs in microseconds, and fails loudly with a useful message if a future
// refactor adds a fourth response builder without the matching wire-up.
func TestBuildCheckInResponse_AllPathsShipManifestEntries(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	src := filepath.Join(filepath.Dir(thisFile), "agentstream.go")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}

	const builderLiteral = "&pb.CheckInResponse{"
	const wireUp = "resp.ManifestEntries = s.collectManifestEntries()"
	const limitsWireUp = "resp.SwarmRuntimeLimits = s.buildSwarmRuntimeLimits()"

	builders := strings.Count(string(body), builderLiteral)
	wireUps := strings.Count(string(body), wireUp)
	limitsWireUps := strings.Count(string(body), limitsWireUp)

	if builders == 0 {
		t.Fatalf("found zero %q literals in agentstream.go — file structure changed; update this test", builderLiteral)
	}
	if builders != wireUps {
		t.Errorf(
			"agentstream.go has %d CheckInResponse builders but only %d %q wire-ups.\n"+
				"Every CheckInResponse return path MUST populate ManifestEntries — without this, "+
				"server-initiated swarm revocations never propagate to agents (the bug fixed in 0.20.7). "+
				"If you added a new builder, add the wire-up before the return. If you intentionally "+
				"want a builder without manifest gossip, refactor this test to whitelist it explicitly.",
			builders, wireUps, wireUp,
		)
	}
	if builders != limitsWireUps {
		t.Errorf(
			"agentstream.go has %d CheckInResponse builders but only %d %q wire-ups.\n"+
				"Every CheckInResponse return path MUST populate SwarmRuntimeLimits — without this, "+
				"per-subsystem caps from server.yaml never reach agents and they fall back to "+
				"compile-time defaults silently. Same wiring discipline as ManifestEntries.",
			builders, limitsWireUps, limitsWireUp,
		)
	}
}
