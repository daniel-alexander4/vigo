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
	if cfg.adminToken == "" {
		die("admin token required (pass --admin-token or make --admin-token-file readable)")
	}

	plan := buildHostPlan(cfg.count, cfg.brokenCount)
	fmt.Printf("planned %d synthetic envoys across %d regions (%d broken)\n", len(plan), len(synthRegions), cfg.brokenCount)

	cleanupDirs, err := writeConfigTree(cfg.stacksDir, cfg.fleetDir, plan)
	if err != nil {
		die("write config tree: %v", err)
	}
	fmt.Printf("wrote synthetic config under:\n  %s\n", strings.Join(cleanupDirs, "\n  "))

	if err := publishConfig(cfg); err != nil {
		die("publish synthetic config: %v", err)
	}
	fmt.Println("published synthetic config")

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

	st := &fleetState{
		Version:     stateVersion,
		Count:       len(enrolled),
		StacksDir:   cfg.stacksDir,
		CleanupDirs: cleanupDirs,
		Hosts:       enrolled,
	}
	if err := saveState(cfg.statePath, st); err != nil {
		die("save state manifest: %v", err)
	}
	fmt.Printf("enrolled %d envoys; manifest at %s\n", len(enrolled), cfg.statePath)

	fmt.Println("running first freshness tick...")
	runTick(cfg)

	fmt.Printf(`
synthetic fleet is up. To keep it fresh across reboots and server
restarts, install + enable the timer (units in test/fakefleet/):

  sudo cp test/fakefleet/vigo-fakefleet.{service,timer} /etc/systemd/system/
  sudo systemctl daemon-reload
  sudo systemctl enable --now vigo-fakefleet.timer

Tear down with:  vigo-fakefleet backout   (then: systemctl disable --now vigo-fakefleet.timer)
`)
}
