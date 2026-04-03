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
	Use:   "copy <module-name|all>",
	Short: "Copy example modules into stockpile for publishing",
	Long: `Copy example .vgo files from the example-stockpile directory into stockpile/,
preserving directory structure and stripping the .example suffix.

Use "all" to copy every available example.

Examples:
  vigocli config examples copy nginx
  vigocli config examples copy ssh-hardening
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

	// Find matching example by module name (basename without .vgo.example).
	var match *exampleFile
	for i, ex := range examples {
		exName := strings.TrimSuffix(filepath.Base(ex.rel), ".vgo.example")
		if exName == name {
			match = &examples[i]
			break
		}
	}

	if match == nil {
		fmt.Printf("No example found for %q.\n\n", name)
		var available []string
		for _, ex := range examples {
			exName := strings.TrimSuffix(filepath.Base(ex.rel), ".vgo.example")
			if strings.Contains(exName, name) || strings.Contains(name, exName) {
				available = append(available, exName)
			}
		}
		if len(available) > 0 {
			fmt.Println("Did you mean:")
			for _, a := range available {
				fmt.Printf("  %s\n", a)
			}
		} else {
			fmt.Println("Use 'vigocli config examples list' to see available examples.")
		}
		return fmt.Errorf("example not found: %s", name)
	}

	destRel := strings.TrimSuffix(match.rel, ".example")
	dest := filepath.Join(stockpileDir, destRel)

	if _, err := os.Stat(dest); err == nil {
		fmt.Printf("  %s %s (already exists)\n", yellow("skip"), destRel)
		return nil
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
	fmt.Println("\nRun 'vigocli config publish' to activate.")
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
