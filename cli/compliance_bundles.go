// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"git.alexander4.io/vigo/server/compliance"
)

// vigocli compliance bundles — lists the Vigo-curated framework-bundle
// catalog. Bundles are the sibling concept to provisions: provisions
// name what a configcrate *does* (functional, cross-framework); bundles
// name *which framework section* a configcrate satisfies. Two catalogs, two
// CLI verbs, two operator mental models.
var complianceBundlesCmd = &cobra.Command{
	Use:   "bundles",
	Short: "List the Vigo-curated framework-bundle catalog (configcrate bundle: tags for framework-scope-cut attribution)",
	Long: `List the Vigo-curated framework-bundle catalog used by configcrates' "bundle:" field.

Each bundle is a kebab-case tag tied to a specific framework section
(e.g. "cis-ubuntu-access-baseline", "ce-firewalls", "ccss-audit-integrity")
that the loader expands into framework -> control coverage. A configcrate
that exists to implement one framework's rule set declares
"bundle: [cis-ubuntu-access-baseline]"; a configcrate that ships a
functional capability that happens to satisfy controls across many
frameworks uses "provides:" instead.

Examples:
  vigocli compliance bundles                                  List all known bundles.
  vigocli compliance bundles --framework cis-ubuntu           Filter to bundles primarily for CIS Ubuntu.
  vigocli compliance bundles --control 5.4.1.1                Find which bundles satisfy a specific control.
  vigocli compliance bundles --show cis-ubuntu-access-baseline   Show the framework->controls map for one bundle.
`,
	RunE: runComplianceBundles,
}

func runComplianceBundles(cmd *cobra.Command, _ []string) error {
	framework, _ := cmd.Flags().GetString("framework")
	control, _ := cmd.Flags().GetString("control")
	show, _ := cmd.Flags().GetString("show")

	if show != "" {
		b, ok := compliance.ConfigcrateBundles[show]
		if !ok {
			return fmt.Errorf("unknown bundle %q (run `vigocli compliance bundles` to list)", show)
		}
		fmt.Printf("%s %s\n", bold(b.ID), dim("("+b.Title+")"))
		fmt.Println()
		fmt.Println(wrapText(b.Description, 78, "  "))
		fmt.Println()
		fwNames := make([]string, 0, len(b.Frameworks))
		for fw := range b.Frameworks {
			fwNames = append(fwNames, fw)
		}
		sort.Strings(fwNames)
		for _, fw := range fwNames {
			fmt.Printf("  %s  %s\n", bold(fw+":"), strings.Join(b.Frameworks[fw], ", "))
		}
		return nil
	}

	ids := compliance.KnownBundleIDs()
	if len(ids) == 0 {
		fmt.Println("Catalog is empty — no bundles authored yet.")
		return nil
	}
	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "%s\t%s\t%s\n", header("BUNDLE"), header("TITLE"), header("FRAMEWORKS"))

	shown := 0
	for _, id := range ids {
		b := compliance.ConfigcrateBundles[id]

		if framework != "" {
			if _, ok := b.Frameworks[framework]; !ok {
				continue
			}
		}
		if control != "" {
			hit := false
			for _, controls := range b.Frameworks {
				for _, cid := range controls {
					if cid == control {
						hit = true
						break
					}
				}
				if hit {
					break
				}
			}
			if !hit {
				continue
			}
		}

		fwNames := make([]string, 0, len(b.Frameworks))
		for fw := range b.Frameworks {
			fwNames = append(fwNames, fw)
		}
		sort.Strings(fwNames)
		fmt.Fprintf(w, "%s\t%s\t%s\n", b.ID, b.Title, dim(strings.Join(fwNames, ", ")))
		shown++
	}

	if shown == 0 {
		fmt.Println("No bundles matched the filters.")
		return nil
	}
	return w.Flush()
}

func init() {
	complianceBundlesCmd.Flags().String("framework", "", "Filter to bundles that cover this framework slug (e.g. cis-ubuntu, cyber-essentials)")
	complianceBundlesCmd.Flags().String("control", "", "Filter to bundles that include this control ID (e.g. 5.4.1.1, a.1)")
	complianceBundlesCmd.Flags().String("show", "", "Show the full framework->controls map for one bundle")
	complianceCmd.AddCommand(complianceBundlesCmd)
}
