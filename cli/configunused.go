// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.alexander4.io/vigo/server/publish"
	"github.com/spf13/cobra"
)

var configUnusedCmd = &cobra.Command{
	Use:   "unused",
	Short: "Find unused modules and match files in the config tree",
	Long: `Scans the config tree for module definitions that are never referenced
by any role, match entry, common.vgo, or environments.vgo, and for match
files whose patterns don't match any enrolled envoy.

Examples:
  vigocli config unused
  vigocli config unused --json
  vigocli config unused --delete`,
	RunE: runConfigUnused,
}

func init() {
	configUnusedCmd.Flags().Bool("json", false, "output raw JSON")
	configUnusedCmd.Flags().Bool("delete", false, "prompt to delete unused files from stockpile")
	configCmd.AddCommand(configUnusedCmd)
}

func runConfigUnused(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	var result struct {
		UnusedModules []struct {
			Name       string `json:"name"`
			SourceFile string `json:"source_file"`
		} `json:"unused_modules"`
		UnusedMatchFiles []struct {
			Path     string   `json:"path"`
			Patterns []string `json:"patterns"`
		} `json:"unused_match_files"`
	}

	if err := client.get("/api/v1/config/unused", &result); err != nil {
		return err
	}

	jsonMode, _ := cmd.Flags().GetBool("json")
	if jsonMode {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	totalModules := len(result.UnusedModules)
	totalFiles := len(result.UnusedMatchFiles)

	if totalModules == 0 && totalFiles == 0 {
		fmt.Println(green("No unused modules or match files found."))
		return nil
	}

	if totalModules > 0 {
		fmt.Printf("\n%s  %d unused module(s)\n\n", amber("!"), totalModules)
		w := newAlignedTable(os.Stdout)
		fmt.Fprintf(w, "  NAME\tFILE\n")
		for _, m := range result.UnusedModules {
			fmt.Fprintf(w, "  %s\t%s\n", amber(m.Name), medGray(m.SourceFile))
		}
		w.Flush()
	}

	if totalFiles > 0 {
		fmt.Printf("\n%s  %d unused match file(s)\n\n", amber("!"), totalFiles)
		w := newAlignedTable(os.Stdout)
		fmt.Fprintf(w, "  FILE\tPATTERNS\n")
		for _, f := range result.UnusedMatchFiles {
			fmt.Fprintf(w, "  %s\t%s\n", amber(f.Path), medGray(strings.Join(f.Patterns, ", ")))
		}
		w.Flush()
	}

	fmt.Println()

	deleteMode, _ := cmd.Flags().GetBool("delete")
	if !deleteMode {
		return nil
	}

	// Collect unique files to delete.
	toDelete := make(map[string]bool)
	for _, m := range result.UnusedModules {
		if m.SourceFile != "" {
			toDelete[m.SourceFile] = true
		}
	}
	for _, f := range result.UnusedMatchFiles {
		toDelete[f.Path] = true
	}

	if len(toDelete) == 0 {
		return nil
	}

	fmt.Printf("Files to delete from %s:\n", bold(publish.StockpileDir))
	for path := range toDelete {
		fmt.Printf("  %s\n", path)
	}
	fmt.Println()

	if !confirmAction(fmt.Sprintf("Delete %d file(s) from stockpile?", len(toDelete))) {
		fmt.Println("Cancelled.")
		return nil
	}

	var deleted int
	for path := range toDelete {
		fullPath := filepath.Join(publish.StockpileDir, path)
		if err := os.Remove(fullPath); err != nil {
			fmt.Fprintf(os.Stderr, "  %s  %s: %v\n", red("x"), path, err)
		} else {
			fmt.Printf("  %s  %s\n", green("✓"), path)
			deleted++
		}
	}

	fmt.Printf("\nDeleted %d/%d files.\n", deleted, len(toDelete))
	if deleted > 0 {
		fmt.Printf("Run %s to sync changes to .live/\n", bold("vigocli config publish"))
	}

	return nil
}
