// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"git.alexander4.io/vigo/server/publish"
	"github.com/spf13/cobra"
)

// Default example directory inside the container.
const defaultExampleDir = publish.DefaultStacksExamplesDir

var configExamplesCmd = &cobra.Command{
	Use:   "examples",
	Short: "Browse, copy, and organize example configcrates",
}

var examplesCopyCmd = &cobra.Command{
	Use:   "copy <configcrate-name|directory-name|all>",
	Short: "Copy example configcrates into stacks for publishing",
	Long: `Copy example .vgo files from stacks/examples/ into stacks/,
preserving directory structure and stripping the .example suffix.

Accepts a configcrate name (copies one file) or a directory name (copies all
configcrates in that directory). Use "all" to copy every available example.

Examples:
  vigocli config examples copy nginx
  vigocli config examples copy cis-ubuntu
  vigocli config examples copy compliance
  vigocli config examples copy all`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		exampleDir, _ := cmd.Flags().GetString("example-dir")

		if args[0] == "all" {
			return runCopyAll(publish.DefaultStacksDir, exampleDir)
		}
		return runCopyOne(args[0], publish.DefaultStacksDir, exampleDir)
	},
}

var examplesListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show available example configcrates",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		exampleDir, _ := cmd.Flags().GetString("example-dir")
		return runListExamples(exampleDir)
	},
}

func runListExamples(exampleDirs ...string) error {
	examples, err := findExamples(exampleDirs...)
	if err != nil {
		return err
	}
	if len(examples) == 0 {
		fmt.Println("No examples found.")
		return nil
	}

	fmt.Printf("Available examples (%d):\n\n", len(examples))
	for _, ex := range examples {
		name := strings.TrimSuffix(filepath.Base(ex.rel), ".vgo.example")
		dir := filepath.Dir(ex.rel)
		fmt.Printf("  %-30s %s\n", green(name), dim(dir))
	}
	fmt.Printf("\nCopy with: vigocli config examples copy <name>\n")
	return nil
}

func runCopyAll(stacksDir string, exampleDirs ...string) error {
	examples, err := findExamples(exampleDirs...)
	if err != nil {
		return err
	}

	copied, skipped := 0, 0
	for _, ex := range examples {
		destRel := strings.TrimSuffix(ex.rel, ".example")
		dest := filepath.Join(stacksDir, destRel)

		if _, err := os.Stat(dest); err == nil {
			fmt.Printf("  %s %s (already exists)\n", dim("skip"), destRel)
			skipped++
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return fmt.Errorf("creating directory: %w", err)
		}
		data, err := os.ReadFile(ex.abs)
		if err != nil {
			return fmt.Errorf("reading %s: %w", ex.rel, err)
		}
		if err := os.WriteFile(dest, data, 0644); err != nil {
			return fmt.Errorf("writing %s: %w", destRel, err)
		}
		fmt.Printf("  %s %s\n", green("copy"), destRel)
		copied++
	}

	fmt.Printf("\n%d copied, %d skipped. Run 'vigocli config publish' to activate.\n", copied, skipped)
	return nil
}

func runCopyOne(name, stacksDir string, exampleDirs ...string) error {
	examples, err := findExamples(exampleDirs...)
	if err != nil {
		return err
	}

	// Try exact module name match first (basename without .vgo.example).
	var matches []exampleFile
	for _, ex := range examples {
		exName := strings.TrimSuffix(filepath.Base(ex.rel), ".vgo.example")
		if exName == name {
			matches = append(matches, ex)
			break
		}
	}

	// If no exact match, try directory match — copy all examples in that directory.
	if len(matches) == 0 {
		for _, ex := range examples {
			dir := filepath.Dir(ex.rel)
			// Match against any path component: "cis-ubuntu" matches "configcrates/compliance/cis-ubuntu/"
			if filepath.Base(dir) == name || strings.Contains(dir, "/"+name+"/") || strings.HasPrefix(dir, name+"/") || dir == name {
				matches = append(matches, ex)
			}
		}
	}

	if len(matches) == 0 {
		fmt.Printf("No example found for %q.\n\n", name)
		var suggestions []string
		seen := make(map[string]bool)
		for _, ex := range examples {
			exName := strings.TrimSuffix(filepath.Base(ex.rel), ".vgo.example")
			dir := filepath.Base(filepath.Dir(ex.rel))
			if strings.Contains(exName, name) || strings.Contains(name, exName) {
				if !seen[exName] {
					suggestions = append(suggestions, exName)
					seen[exName] = true
				}
			}
			if strings.Contains(dir, name) || strings.Contains(name, dir) {
				if !seen[dir] {
					suggestions = append(suggestions, dir+" (directory)")
					seen[dir] = true
				}
			}
		}
		if len(suggestions) > 0 {
			fmt.Println("Did you mean:")
			for _, s := range suggestions {
				fmt.Printf("  %s\n", s)
			}
		} else {
			fmt.Println("Use 'vigocli config examples list' to see available examples.")
		}
		return fmt.Errorf("example not found: %s", name)
	}

	copied, skipped := 0, 0
	for _, match := range matches {
		destRel := strings.TrimSuffix(match.rel, ".example")
		dest := filepath.Join(stacksDir, destRel)

		if _, err := os.Stat(dest); err == nil {
			fmt.Printf("  %s %s (already exists)\n", yellow("skip"), destRel)
			skipped++
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return fmt.Errorf("creating directory: %w", err)
		}
		data, err := os.ReadFile(match.abs)
		if err != nil {
			return fmt.Errorf("reading %s: %w", match.rel, err)
		}
		if err := os.WriteFile(dest, data, 0644); err != nil {
			return fmt.Errorf("writing %s: %w", destRel, err)
		}
		fmt.Printf("  %s %s\n", green("copy"), destRel)
		copied++
	}

	if copied > 0 || skipped > 0 {
		fmt.Printf("\n%d copied, %d skipped. Run 'vigocli config publish' to activate.\n", copied, skipped)
	}
	return nil
}

func runOrganize(stacksDir string, exampleDirs ...string) error {
	examples, err := findExamples(exampleDirs...)
	if err != nil {
		return err
	}

	examplePaths := make(map[string]string) // "ssh-hardening" → "configcrates/system/ssh-hardening.vgo"
	for _, ex := range examples {
		name := strings.TrimSuffix(filepath.Base(ex.rel), ".vgo.example")
		destRel := strings.TrimSuffix(ex.rel, ".example")
		examplePaths[name] = destRel
	}

	configcratesDir := filepath.Join(stacksDir, "configcrates")
	entries, err := os.ReadDir(configcratesDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No configcrates directory found.")
			return nil
		}
		return err
	}

	moved := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".vgo") {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".vgo")
		exDest, ok := examplePaths[name]
		if !ok {
			continue
		}

		currentRel := filepath.Join("configcrates", entry.Name())
		if currentRel == exDest {
			continue
		}

		exDir := filepath.Dir(exDest)
		if exDir == "configcrates" {
			continue
		}

		src := filepath.Join(stacksDir, currentRel)
		dest := filepath.Join(stacksDir, exDest)

		if _, err := os.Stat(dest); err == nil {
			fmt.Printf("  %s %s → %s (destination exists)\n", yellow("skip"), currentRel, exDest)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return fmt.Errorf("creating directory: %w", err)
		}
		if err := os.Rename(src, dest); err != nil {
			return fmt.Errorf("moving %s → %s: %w", currentRel, exDest, err)
		}
		fmt.Printf("  %s %s → %s\n", green("move"), currentRel, exDest)
		moved++
	}

	if moved == 0 {
		fmt.Println("All configcrates are already properly organized.")
	} else {
		fmt.Printf("\n%d configcrate(s) moved. Run 'vigocli config publish' to sync.\n", moved)
	}
	return nil
}

// exampleFile represents a discovered .vgo.example file.
type exampleFile struct {
	abs string // absolute path
	rel string // relative to example dir (e.g., "configcrates/system/ssh-hardening.vgo.example")
}

// findExamples walks one or more *.vgo.example trees and returns a unified
// flat list. Each tree contributes paths relative to its own root so the
// destination logic (strip ".example", join under stacks) works. A missing
// dir is silently skipped; an error is returned only if every passed dir
// is missing.
func findExamples(exampleDirs ...string) ([]exampleFile, error) {
	var examples []exampleFile
	seen := 0
	for _, exampleDir := range exampleDirs {
		if exampleDir == "" {
			continue
		}
		if _, err := os.Stat(exampleDir); os.IsNotExist(err) {
			continue
		}
		seen++
		err := filepath.WalkDir(exampleDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if !strings.HasSuffix(d.Name(), ".vgo.example") {
				return nil
			}
			rel, _ := filepath.Rel(exampleDir, path)
			examples = append(examples, exampleFile{abs: path, rel: rel})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if seen == 0 {
		return nil, fmt.Errorf("no example directories found: %v", exampleDirs)
	}
	sort.Slice(examples, func(i, j int) bool { return examples[i].rel < examples[j].rel })
	return examples, nil
}

func init() {
	configExamplesCmd.PersistentFlags().String("example-dir", defaultExampleDir, "Path to the stacks-examples directory")

	configExamplesCmd.AddCommand(examplesCopyCmd, examplesListCmd)
	configCmd.AddCommand(configExamplesCmd)
}
