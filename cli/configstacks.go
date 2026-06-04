// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.alexander4.io/vigo/server/publish"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var configStacksCmd = &cobra.Command{
	Use:   "stacks",
	Short: "Manage the stacks directory",
}

var stacksOrganizeCmd = &cobra.Command{
	Use:   "organize",
	Short: "Move flat configcrates into proper subdirectories based on example layout",
	Long: `Scans stacks/configcrates/ for .vgo files at the top level and moves them
into the subdirectory structure used by the example configcrates.

For example, if stacks/configcrates/ssh-hardening.vgo exists and the example
layout has configcrates/system/ssh-hardening.vgo.example, the file is moved to
stacks/configcrates/system/ssh-hardening.vgo.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		exampleDir, _ := cmd.Flags().GetString("example-dir")
		return runOrganize(publish.DefaultStacksDir, exampleDir)
	},
}

var stacksSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search configcrates in the stacks by name, resource, or content",
	Long: `Search stacks/configcrates/ for .vgo files matching a query string.
Matches against configcrate name, description, resource names, resource types,
and attribute values (target_path, package, service, command, etc.).

Examples:
  vigocli config stacks search nginx
  vigocli config stacks search firewall
  vigocli config stacks search /etc/ssh`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStacksSearch(args[0], publish.DefaultStacksDir)
	},
}

// stacksModule is a minimal struct for searching module content.
type stacksModule struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Resources   []struct {
		Name  string         `yaml:"name"`
		Type  string         `yaml:"type"`
		Attrs map[string]any `yaml:",inline"`
	} `yaml:"resources"`
}

func runStacksSearch(query, stacksDir string) error {
	modulesDir := filepath.Join(stacksDir, "configcrates")
	query = strings.ToLower(query)

	var results []searchResult
	err := filepath.WalkDir(modulesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".vgo") {
			return err
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}

		rel, _ := filepath.Rel(stacksDir, path)
		matches := searchStacksModule(query, rel, data)
		if len(matches) > 0 {
			results = append(results, searchResult{path: rel, matches: matches})
		}
		return nil
	})
	if err != nil {
		return err
	}

	if len(results) == 0 {
		fmt.Printf("No configcrates matching %q in stacks.\n", query)
		return nil
	}

	fmt.Printf("Found %d configcrate(s) matching %q:\n\n", len(results), query)
	for _, r := range results {
		fmt.Printf("  %s\n", green(r.path))
		for _, m := range r.matches {
			fmt.Printf("    %s %s\n", dim("•"), m)
		}
		fmt.Println()
	}
	return nil
}

type searchResult struct {
	path    string
	matches []string
}

func searchStacksModule(query, relPath string, data []byte) []string {
	var matches []string

	// Match on filename.
	if strings.Contains(strings.ToLower(filepath.Base(relPath)), query) {
		matches = append(matches, "filename match")
	}

	var mod stacksModule
	if err := yaml.Unmarshal(data, &mod); err != nil {
		// Can't parse — fall back to raw text search.
		if strings.Contains(strings.ToLower(string(data)), query) {
			matches = append(matches, "raw content match")
		}
		return matches
	}

	if strings.Contains(strings.ToLower(mod.Name), query) {
		matches = append(matches, fmt.Sprintf("name: %s", mod.Name))
	}
	if strings.Contains(strings.ToLower(mod.Description), query) {
		matches = append(matches, fmt.Sprintf("description: %s", mod.Description))
	}

	for _, res := range mod.Resources {
		if strings.Contains(strings.ToLower(res.Name), query) {
			matches = append(matches, fmt.Sprintf("resource: %s (%s)", res.Name, res.Type))
		}
		if strings.Contains(strings.ToLower(res.Type), query) {
			matches = append(matches, fmt.Sprintf("type: %s (resource %s)", res.Type, res.Name))
		}
		// Search string attribute values.
		for key, val := range res.Attrs {
			s, ok := val.(string)
			if !ok {
				continue
			}
			if strings.Contains(strings.ToLower(s), query) {
				matches = append(matches, fmt.Sprintf("%s.%s: %s", res.Name, key, truncate(s, 60)))
			}
		}
	}

	return matches
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func init() {
	stacksOrganizeCmd.Flags().String("example-dir", defaultExampleDir, "Path to stacks/examples directory")

	configStacksCmd.AddCommand(stacksOrganizeCmd, stacksSearchCmd)
	configCmd.AddCommand(configStacksCmd)
}
