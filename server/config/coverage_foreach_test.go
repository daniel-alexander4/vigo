// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"path/filepath"
	"testing"
)

// TestForeachInstanceContributesCoverage: a foreach instance
// ("vhost[www.example.com]") covers the controls its definition ("vhost")
// claims, exactly as a plain reference would.
func TestForeachInstanceContributesCoverage(t *testing.T) {
	dir := setupDir(t)
	writeVhostConfigcrate(t, dir)
	writeFile(t, filepath.Join(dir, "configcrates", "compliance.vgo"), "compliance:\n  pci-dss: [\"2.2.1\"]\n")
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web01"
    configcrates:
      - name: vhost
        foreach: vhosts
        key: server_name
    vars:
      vhosts:
        - server_name: www.example.com
`)
	store := storeFromDir(t, dir)
	cov := store.EnvoyStandardCoverage("web01", "pci-dss")
	if cov == nil {
		t.Fatal("no pci-dss coverage for web01")
	}
	if len(cov.CoveredControls) != 1 || cov.CoveredControls[0].ControlID != "2.2.1" {
		t.Fatalf("foreach instance covered %+v, want control 2.2.1 via vhost", cov.CoveredControls)
	}
}
