// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

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
	envoys, err := listAllEnvoys(client, false)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var names []string
	for _, e := range envoys {
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
	envoys, err := listAllEnvoys(client, false)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var completions []string
	for _, e := range envoys {
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

// completeConfigcrateNames scans the stacks for .vgo module names.
func completeConfigcrateNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	configcratesDir := filepath.Join(vigoConfigRoot, "configcrates")
	var names []string
	_ = filepath.WalkDir(configcratesDir, func(path string, d os.DirEntry, err error) error {
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
