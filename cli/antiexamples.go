// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Anti-examples mirror the examples/ tree with retract templates —
// every module example at stacks/examples/modules/<cat>/<name>.vgo.example
// has a paired retract at stacks/anti-examples/modules/<cat>/<name>-retract.vgo.example.
// The mirror-by-construction makes it structurally obvious if a retract
// is missing for a given install template.

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

// defaultAntiExampleDir is the read-only retraction-template tree
// inside the container, sibling to examples/. Seeded from the image
// at /seed/example-configs/retractions/ on first boot.
const defaultAntiExampleDir = "/srv/vigo/stacks/anti-examples"

var configAntiExamplesCmd = &cobra.Command{
	Use:   "anti-examples",
	Short: "Browse, copy, and verify retract templates that mirror examples/",
	Long: `Anti-examples are retract templates paired with each module example.
The directory tree mirrors examples/modules/ so a missing retract is
visually obvious. Use mirror-check to scan for examples without
matching anti-examples.`,
}

var antiExamplesListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show available retract templates",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, _ := cmd.Flags().GetString("anti-example-dir")
		examples, err := findExamples(dir)
		if err != nil {
			return err
		}
		if len(examples) == 0 {
			fmt.Println("No anti-examples found.")
			return nil
		}
		fmt.Printf("Available retract templates (%d):\n\n", len(examples))
		for _, ex := range examples {
			name := strings.TrimSuffix(filepath.Base(ex.rel), ".vgo.example")
			subdir := filepath.Dir(ex.rel)
			fmt.Printf("  %-40s %s\n", green(name), dim(subdir))
		}
		fmt.Printf("\nCopy with: vigocli config anti-examples copy <name>\n")
		return nil
	},
}

var antiExamplesCopyCmd = &cobra.Command{
	Use:   "copy <retract-name|directory-name|all>",
	Short: "Copy retract templates into stockpile alongside their modules",
	Long: `Copy retract .vgo.example files from stacks/anti-examples/ into
stacks/stockpile/, preserving the directory structure and stripping
the .example suffix. Filenames keep their -retract suffix so a
retract drops in alongside its install module without colliding
(e.g., postgres.vgo + postgres-retract.vgo in the same directory).

Examples:
  vigocli config anti-examples copy postgres-retract
  vigocli config anti-examples copy databases
  vigocli config anti-examples copy all`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, _ := cmd.Flags().GetString("anti-example-dir")
		if args[0] == "all" {
			return runCopyAll(publish.StockpileDir, dir)
		}
		return runCopyOne(args[0], publish.StockpileDir, dir)
	},
}

var antiExamplesMirrorCheckCmd = &cobra.Command{
	Use:   "mirror-check",
	Short: "Report module examples without a matching anti-example (and vice versa)",
	Long: `Walk examples/modules/ and anti-examples/modules/ and report
mismatches: install templates without a paired retract, or retracts
referencing a module example that no longer exists. The /tidy
contract requires every module example to ship with a matching
retract; this check makes the missing pieces visible.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		exDir, _ := cmd.Flags().GetString("example-dir")
		antiDir, _ := cmd.Flags().GetString("anti-example-dir")
		return runMirrorCheck(exDir, antiDir)
	},
}

// runMirrorCheck pairs every example under examples/modules/ with a
// retract under anti-examples/modules/ and reports gaps in either
// direction. Pair logic: an example at examples/modules/<sub>/<name>.vgo.example
// pairs with anti-examples/modules/<sub>/<name>-retract.vgo.example.
// Returns a non-nil error when any gap exists so the command can be
// wired into CI.
func runMirrorCheck(exampleDir, antiExampleDir string) error {
	exMods := collectModuleExamples(exampleDir)
	antiMods := collectModuleAntiExamples(antiExampleDir)

	var missingRetracts []string
	for rel := range exMods {
		expectedRetract := retractRelFromExampleRel(rel)
		if _, ok := antiMods[expectedRetract]; !ok {
			missingRetracts = append(missingRetracts, rel)
		}
	}
	var orphanRetracts []string
	for rel := range antiMods {
		expectedExample := exampleRelFromRetractRel(rel)
		if _, ok := exMods[expectedExample]; !ok {
			orphanRetracts = append(orphanRetracts, rel)
		}
	}

	sort.Strings(missingRetracts)
	sort.Strings(orphanRetracts)

	if len(missingRetracts) == 0 && len(orphanRetracts) == 0 {
		fmt.Printf("%s %d module examples, all paired with anti-examples.\n",
			green("OK"), len(exMods))
		return nil
	}

	if len(missingRetracts) > 0 {
		fmt.Printf("%s module examples without a matching anti-example (%d):\n",
			yellow("MISSING"), len(missingRetracts))
		for _, p := range missingRetracts {
			expected := retractRelFromExampleRel(p)
			fmt.Printf("  %s\n    expected: anti-examples/%s\n", p, expected)
		}
	}
	if len(orphanRetracts) > 0 {
		if len(missingRetracts) > 0 {
			fmt.Println()
		}
		fmt.Printf("%s anti-examples without a matching module example (%d):\n",
			yellow("ORPHAN"), len(orphanRetracts))
		for _, p := range orphanRetracts {
			expected := exampleRelFromRetractRel(p)
			fmt.Printf("  %s\n    expected: examples/%s\n", p, expected)
		}
	}
	return fmt.Errorf("mirror-check found %d missing + %d orphan",
		len(missingRetracts), len(orphanRetracts))
}

// collectModuleExamples walks examples/modules/ and returns rel paths
// (relative to examples/) of every *.vgo.example file. Empty map if
// the dir doesn't exist.
func collectModuleExamples(exampleDir string) map[string]struct{} {
	out := make(map[string]struct{})
	root := filepath.Join(exampleDir, "modules")
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".vgo.example") {
			return nil
		}
		// Skip retract templates that may have leaked into examples/.
		if strings.HasSuffix(d.Name(), "-retract.vgo.example") {
			return nil
		}
		rel, _ := filepath.Rel(exampleDir, path)
		out[filepath.ToSlash(rel)] = struct{}{}
		return nil
	})
	return out
}

// collectModuleAntiExamples is the symmetric walker for the
// anti-examples/modules/ tree. Only counts files ending in
// -retract.vgo.example.
func collectModuleAntiExamples(antiExampleDir string) map[string]struct{} {
	out := make(map[string]struct{})
	root := filepath.Join(antiExampleDir, "modules")
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), "-retract.vgo.example") {
			return nil
		}
		rel, _ := filepath.Rel(antiExampleDir, path)
		out[filepath.ToSlash(rel)] = struct{}{}
		return nil
	})
	return out
}

// retractRelFromExampleRel maps an examples/-relative rel like
// "modules/databases/postgres.vgo.example" to its expected
// anti-examples/-relative rel "modules/databases/postgres-retract.vgo.example".
func retractRelFromExampleRel(rel string) string {
	return strings.TrimSuffix(rel, ".vgo.example") + "-retract.vgo.example"
}

// exampleRelFromRetractRel is the inverse of retractRelFromExampleRel.
func exampleRelFromRetractRel(rel string) string {
	stem := strings.TrimSuffix(rel, "-retract.vgo.example")
	return stem + ".vgo.example"
}

func init() {
	configAntiExamplesCmd.PersistentFlags().String("anti-example-dir", defaultAntiExampleDir, "Path to stacks/anti-examples directory")
	// mirror-check needs both roots; reuse the example-dir flag from
	// the examples command via a local definition so the subcommand is
	// self-contained.
	antiExamplesMirrorCheckCmd.Flags().String("example-dir", defaultExampleDir, "Path to stacks/examples directory")

	configAntiExamplesCmd.AddCommand(antiExamplesCopyCmd, antiExamplesListCmd, antiExamplesMirrorCheckCmd)
	configCmd.AddCommand(configAntiExamplesCmd)
}
