// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// completeHostnames queries the server for enrolled envoy hostnames.
func completeHostnames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	client := newAPIClient()
	var resp struct {
		Envoys []struct {
			Hostname string `json:"hostname"`
		} `json:"envoys"`
	}
	if err := client.get("/api/v1/envoys", &resp); err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var names []string
	for _, e := range resp.Envoys {
		names = append(names, e.Hostname)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// completeEnvoyIDs returns hostnames and IDs for commands that accept either.
func completeEnvoyIDs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	client := newAPIClient()
	var resp struct {
		Envoys []struct {
			ID       string `json:"id"`
			Hostname string `json:"hostname"`
		} `json:"envoys"`
	}
	if err := client.get("/api/v1/envoys", &resp); err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var completions []string
	for _, e := range resp.Envoys {
		completions = append(completions, e.Hostname)
		completions = append(completions, e.ID)
	}
	return completions, cobra.ShellCompDirectiveNoFileComp
}

// completeRunIDs returns recent run IDs.
func completeRunIDs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	client := newAPIClient()
	var resp struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}
	if err := client.get("/api/v1/runs?limit=20", &resp); err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var ids []string
	for _, r := range resp.Runs {
		ids = append(ids, r.ID)
	}
	return ids, cobra.ShellCompDirectiveNoFileComp
}

// completeModuleNames scans the stockpile for .vgo module names.
func completeModuleNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	modulesDir := filepath.Join(vigoConfigRoot, "worksite", "stockpile", "modules")
	var names []string
	_ = filepath.WalkDir(modulesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(d.Name(), ".vgo") {
			names = append(names, strings.TrimSuffix(d.Name(), ".vgo"))
		}
		return nil
	})
	return names, cobra.ShellCompDirectiveNoFileComp
}
