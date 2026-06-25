// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// runCreate stands the fleet up end to end: generate the synthetic
// config tree, publish it, enroll every host, persist the identity
// manifest, and run a first freshness tick. After it returns the
// operator enables the systemd timer to keep the fleet fresh.
func runCreate(cfg config) {
	// Enrollment is tokenless on a trusted CIDR — the server auto-signs from a
	// private/loopback source. The admin token is only needed to publish the
	// config tree, so --skip-publish (enroll against the server's existing
	// config) needs no token.
	if !cfg.skipPublish && cfg.adminToken == "" {
		die("admin token required to publish the config tree (or pass --skip-publish to enroll against the server's existing config)")
	}

	plan := buildHostPlan(cfg.count, cfg.namedCount, cfg.brokenCount, cfg.changedCount, cfg.hostnameSuffix)
	named := 0
	for _, h := range plan {
		if !strings.HasPrefix(h.Hostname, "worker-") {
			named++
		}
	}
	fmt.Printf("planned %d envoys across %d regions (%d named, %d worker; %d failed, %d changed)\n",
		len(plan), len(synthRegions), named, len(plan)-named, cfg.brokenCount, cfg.changedCount)

	// --skip-publish enrolls against whatever config the target server
	// already serves — used by the ephemeral CLI smoke harness, whose
	// co-located vigocli config publish is hardwired to /srv/vigo and would
	// otherwise read prod stacks + write prod .live. Skip both the config-tree
	// write and the publish; the synthetic envoys still enroll and report.
	var cleanupDirs []string
	if !cfg.skipPublish {
		var err error
		cleanupDirs, err = writeConfigTree(cfg.stacksDir, cfg.fleetDir, plan)
		if err != nil {
			die("write config tree: %v", err)
		}
		fmt.Printf("wrote fleet config under:\n  %s\n", strings.Join(cleanupDirs, "\n  "))

		if err := publishConfig(cfg); err != nil {
			die("publish fleet config: %v", err)
		}
		fmt.Println("published fleet config")
	}

	hosts := make([]hostState, len(plan))
	var firstErr struct {
		sync.Once
		msg string
	}
	failed := runPool(len(plan), cfg.concurrency, "enroll", func(i int) error {
		e, err := enroll(cfg.httpClient, cfg.apiAddr, plan[i].Hostname, plan[i].IP)
		if err != nil {
			firstErr.Do(func() { firstErr.msg = err.Error() })
			return err
		}
		hosts[i] = hostState{
			hostPlan:   plan[i],
			EnvoyID:    e.id,
			SigningKey: []byte(e.signingKey),
			CertPEM:    e.certPEM,
			KeyPEM:     e.keyPEM,
			CACertPEM:  e.caCertPEM,
		}
		return nil
	})

	enrolled := hosts[:0]
	for i := range hosts {
		if hosts[i].EnvoyID != "" {
			enrolled = append(enrolled, hosts[i])
		}
	}
	if len(enrolled) == 0 {
		fmt.Fprintf(os.Stderr, "first enroll error: %s\n", firstErr.msg)
		die("no envoys enrolled — likely the license node-count gate (needs MaxEnvoys >= %d)", cfg.count)
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d/%d enrollments failed (first: %s)\n", failed, len(plan), firstErr.msg)
	}

	invKB := cfg.inventoryKB
	if invKB <= 0 {
		invKB = defaultInventoryKB
	}
	st := &fleetState{
		Version:       stateVersion,
		Count:         len(enrolled),
		StacksDir:     cfg.stacksDir,
		CleanupDirs:   cleanupDirs,
		InventoryBulk: genInventoryBulk(invKB),
		Vitals:        genVitals(0), // representative size sample for the manifest; the wire vitals are fresh per tick (freshVitals)
		Hosts:         enrolled,
	}
	// Install the shared payloads (0 = use the bulk just baked above), then stamp
	// each envoy's stable inventory hash (identity ∪ bulk) so steady-state
	// vitals-only check-ins keep hitting the server's per-envoy inventory cache.
	loadPayloads(st, 0)
	for i := range st.Hosts {
		st.Hosts[i].InventoryHash = inventoryHashFor(&st.Hosts[i])
	}
	fmt.Printf("split-traits payload: inventory %d KB (shared bulk %d KB + per-host identity), vitals %d KB\n",
		(len(st.InventoryBulk)+len(st.Hosts[0].IdentityJSON))/1024, len(st.InventoryBulk)/1024, len(st.Vitals)/1024)
	if err := saveState(cfg.statePath, st); err != nil {
		die("save state manifest: %v", err)
	}
	fmt.Printf("enrolled %d envoys; manifest at %s\n", len(enrolled), cfg.statePath)

	fmt.Println("running first report pass (populates convergence)...")
	runTick(cfg)

	fmt.Printf(`
synthetic fleet is enrolled. Start the daemon to drive check-ins (default 1s
cadence, persistent connection per envoy):

  sudo cp test/fakefleet/vigo.service /etc/systemd/system/
  sudo systemctl daemon-reload
  sudo systemctl enable --now vigo.service

Or run it in the foreground:  sudo vigo run

Tear down with:  vigo backout   (then: systemctl disable --now vigo.service)
`)
}
