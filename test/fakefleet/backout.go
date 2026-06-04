// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// runBackout removes every trace of the synthetic fleet: deletes the
// envoys (keyed on the reserved `.synthetic` suffix so it can never touch
// a real one), wipes the two synthetic config dirs and republishes, and
// removes the state manifest. Safe to run whether or not the timer is
// still firing — a tick that races the delete just logs NotFound.
func runBackout(cfg config) {
	if cfg.adminToken == "" {
		die("admin token required (pass --admin-token or make --admin-token-file readable)")
	}

	// Prefer the manifest's ids; fall back to listing the server and
	// filtering by suffix if the manifest is lost.
	stacksDir := cfg.stacksDir
	var ids []string
	if st, err := loadState(cfg.statePath); err == nil {
		for _, h := range st.Hosts {
			ids = append(ids, h.EnvoyID)
		}
		if st.StacksDir != "" {
			stacksDir = st.StacksDir
		}
		fmt.Printf("manifest lists %d synthetic envoys\n", len(ids))
	} else {
		fmt.Println("no manifest — listing the server and filtering by suffix")
		ids, err = listSyntheticEnvoyIDs(cfg)
		if err != nil {
			die("list envoys: %v", err)
		}
		fmt.Printf("found %d *.%s envoys server-side\n", len(ids), synthTLD)
	}

	if len(ids) > 0 {
		runPool(len(ids), cfg.concurrency, "delete", func(i int) error {
			return deleteEnvoy(cfg.httpClient, cfg.apiAddr, cfg.adminToken, ids[i])
		})
	}

	// Wipe the synthetic config (both namespaced dirs) and republish so
	// the server drops it from .live/.
	for _, dir := range []string{
		filepath.Join(stacksDir, "configcrates", synthTLD),
		filepath.Join(stacksDir, synthTLD),
	} {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: remove %s: %v\n", dir, err)
		} else {
			fmt.Printf("removed %s\n", dir)
		}
	}
	if err := publishConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: republish after wipe: %v\n", err)
	}

	if err := os.Remove(cfg.statePath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: remove manifest: %v\n", err)
	}
	os.Remove(filepath.Dir(cfg.statePath)) // best-effort: drop the dir if now empty

	fmt.Printf("\nbackout complete. If the timer is installed, stop it:\n  sudo systemctl disable --now vigo-synthfleet.timer\n")
}

// listSyntheticEnvoyIDs GETs /api/v1/envoys and returns the ids of every
// envoy whose hostname carries the reserved suffix.
func listSyntheticEnvoyIDs(cfg config) ([]string, error) {
	req, _ := http.NewRequest("GET", cfg.apiAddr+"/api/v1/envoys", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.adminToken)
	resp, err := cfg.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s — %s", resp.Status, string(body))
	}
	var payload struct {
		Envoys []struct {
			ID       string `json:"id"`
			Hostname string `json:"hostname"`
		} `json:"envoys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	suffix := "." + synthTLD
	var ids []string
	for _, e := range payload.Envoys {
		if strings.HasSuffix(e.Hostname, suffix) {
			ids = append(ids, e.ID)
		}
	}
	return ids, nil
}
