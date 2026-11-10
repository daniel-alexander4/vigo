// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc_test

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	isgrpc "git.alexander4.io/vigo/server/grpc"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/registry"
)

// startFleetTestServer is startTestServerWithConfig plus a fleet index, so
// fleet: lookups have members to walk.
func startFleetTestServer(t *testing.T, configDir string) (string, *fleet.FleetIndex, func()) {
	t.Helper()
	for _, table := range []string{"run_results", "runs", "envoy_traits", "plans", "plan_results", "file_snapshots", "approved_patterns", "envoys"} {
		if _, err := testDB.ExecContext(context.Background(), "DELETE FROM "+table); err != nil {
			t.Fatalf("failed to delete from %s: %v", table, err)
		}
	}
	queries := db.New(testDB)
	fi := fleet.NewFleetIndex()
	configStore, err := config.NewConfigStore(configDir, &noopProvider{}, true)
	if err != nil {
		t.Fatal(err)
	}
	srv, _, _, _, _, _, err := isgrpc.NewServer(isgrpc.ServerConfig{
		CertFile:    filepath.Join(testCertDir, "server.crt"),
		KeyFile:     filepath.Join(testCertDir, "server.key"),
		CAFile:      filepath.Join(testCertDir, "ca.crt"),
		GRPCPort:    ":0",
		Registry:    registry.NewRegistry(queries),
		ConfigStore: configStore,
		Queries:     queries,
		FleetIndex:  fi,
		Metrics:     metrics.NewTestMetrics(),
	})
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	return lis.Addr().String(), fi, func() { srv.Stop() }
}

// TestE2E_CaseSeesResolvedFleetVar: a resource case: keyed on a fleet: var
// renders the var's resolved value, not the unresolved lookup struct, so the
// matching branch (not default) is chosen.
func TestE2E_CaseSeesResolvedFleetVar(t *testing.T) {
	configcrates := map[string]string{
		"pick": `
name: pick
resources:
  - name: pick-pkg
    type: package
    state: present
    case: "x{{ .Vars.peers }}x"
    match:
      xx:
        package: resolved
      default:
        package: unresolved
`,
	}
	envoys := `
envoys:
  - match: "*.test.example.com"
    configcrates: [pick]
    vars:
      peers:
        fleet:
          where: "tag('no-such-tag')"
          select: "{{ .Hostname }}"
          join: ","
`
	configDir := setupE2EConfig(t, configcrates, envoys)
	addr, fi, cleanup := startFleetTestServer(t, configDir)
	defer cleanup()

	envoyID, pub, priv, client := registerEnvoy(t, addr, "e2e-case01.test.example.com", "e2e-case-token")
	fi.AddEnvoy(fleet.EnvoyState{ID: envoyID, Hostname: "e2e-case01.test.example.com", CertSPKI: clientCertPin(t)}, pub)
	resp, err := doCheckIn(t, client, envoyID, priv, "fp", 0)
	if err != nil {
		t.Fatalf("CheckIn: %v", err)
	}
	mods := resp.GetPolicyBundle().GetConfigcrates()
	if len(mods) != 1 || len(mods[0].GetResources()) != 1 {
		t.Fatalf("unexpected bundle: %v", mods)
	}
	if got := mods[0].GetResources()[0].GetAttributes()["package"]; got != "resolved" {
		t.Fatalf("package = %q, want resolved (case rendered before the fleet var resolved)", got)
	}
}
