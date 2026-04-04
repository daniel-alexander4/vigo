// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daniel-alexander4/vigo/server/publish"
	"github.com/spf13/cobra"
)

// Default example directory inside the container.
const defaultExampleDir = "/srv/vigo/example-stockpile"

var configExamplesCmd = &cobra.Command{
	Use:   "examples",
	Short: "Browse, copy, and organize example modules",
}

var examplesCopyCmd = &cobra.Command{
	Use:   "copy <module-name|directory-name|all>",
	Short: "Copy example modules into stockpile for publishing",
	Long: `Copy example .vgo files from the example-stockpile directory into stockpile/,
preserving directory structure and stripping the .example suffix.

Accepts a module name (copies one file) or a directory name (copies all
modules in that directory). Use "all" to copy every available example.

Examples:
  vigocli config examples copy nginx
  vigocli config examples copy cis-ubuntu
  vigocli config examples copy compliance
  vigocli config examples copy all`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		exampleDir, _ := cmd.Flags().GetString("example-dir")

		if args[0] == "all" {
			return runCopyAll(exampleDir, publish.StockpileDir)
		}
		return runCopyOne(args[0], exampleDir, publish.StockpileDir)
	},
}

var examplesListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show available example modules",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		exampleDir, _ := cmd.Flags().GetString("example-dir")
		return runListExamples(exampleDir)
	},
}

func runListExamples(exampleDir string) error {
	examples, err := findExamples(exampleDir)
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

func runCopyAll(exampleDir, stockpileDir string) error {
	examples, err := findExamples(exampleDir)
	if err != nil {
		return err
	}

	copied, skipped := 0, 0
	for _, ex := range examples {
		destRel := strings.TrimSuffix(ex.rel, ".example")
		dest := filepath.Join(stockpileDir, destRel)

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

func runCopyOne(name, exampleDir, stockpileDir string) error {
	examples, err := findExamples(exampleDir)
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
			// Match against any path component: "cis-ubuntu" matches "modules/compliance/cis-ubuntu/"
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
		dest := filepath.Join(stockpileDir, destRel)

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

func runOrganize(exampleDir, stockpileDir string) error {
	examples, err := findExamples(exampleDir)
	if err != nil {
		return err
	}

	examplePaths := make(map[string]string) // "ssh-hardening" → "modules/system/ssh-hardening.vgo"
	for _, ex := range examples {
		name := strings.TrimSuffix(filepath.Base(ex.rel), ".vgo.example")
		destRel := strings.TrimSuffix(ex.rel, ".example")
		examplePaths[name] = destRel
	}

	modulesDir := filepath.Join(stockpileDir, "modules")
	entries, err := os.ReadDir(modulesDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No modules directory found.")
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

		currentRel := filepath.Join("modules", entry.Name())
		if currentRel == exDest {
			continue
		}

		exDir := filepath.Dir(exDest)
		if exDir == "modules" {
			continue
		}

		src := filepath.Join(stockpileDir, currentRel)
		dest := filepath.Join(stockpileDir, exDest)

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
		fmt.Println("All modules are already properly organized.")
	} else {
		fmt.Printf("\n%d module(s) moved. Run 'vigocli config publish' to sync.\n", moved)
	}
	return nil
}

// exampleFile represents a discovered .vgo.example file.
type exampleFile struct {
	abs string // absolute path
	rel string // relative to example dir (e.g., "modules/system/ssh-hardening.vgo.example")
}

func findExamples(exampleDir string) ([]exampleFile, error) {
	if _, err := os.Stat(exampleDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("example directory not found: %s", exampleDir)
	}

	var examples []exampleFile
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
	sort.Slice(examples, func(i, j int) bool { return examples[i].rel < examples[j].rel })
	return examples, nil
}

func init() {
	// Shared flag on parent so all subcommands inherit it.
	configExamplesCmd.PersistentFlags().String("example-dir", defaultExampleDir, "Path to example-stockpile directory")

	configExamplesCmd.AddCommand(examplesCopyCmd, examplesListCmd)
	configCmd.AddCommand(configExamplesCmd)
}
