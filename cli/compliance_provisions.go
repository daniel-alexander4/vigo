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

// vigocli compliance provisions — lists the Vigo-curated provision
// catalog. The catalog lives in the binary (server/compliance/
// provisions.go); this command reads it directly rather than going
// through a REST round-trip, so it works offline and on a freshly
// untouched server.
var complianceProvisionsCmd = &cobra.Command{
	Use:   "provisions",
	Short: "List the Vigo-curated provision catalog (configcrate capability tags for compliance attribution)",
	Long: `List the Vigo-curated provision catalog used by configcrates' "provides:" field.

Each provision is a kebab-case tag (e.g. "time-sync", "audit-trail") that
the loader expands into framework -> control coverage. Adding "provides:
[time-sync]" to a configcrate's header replaces a hand-rolled compliance: block
listing the same controls across HIPAA / PCI / NIST / ISO / CIS — the
cross-walk lives once, in this catalog.

Examples:
  vigocli compliance provisions                       List all known provisions.
  vigocli compliance provisions --framework pci-dss   Filter to provisions that cover PCI controls.
  vigocli compliance provisions --control 10.6.1      Find which provisions satisfy a specific control.
  vigocli compliance provisions --show time-sync      Show the framework->controls map for one provision.
`,
	RunE: runComplianceProvisions,
}

func runComplianceProvisions(cmd *cobra.Command, _ []string) error {
	framework, _ := cmd.Flags().GetString("framework")
	control, _ := cmd.Flags().GetString("control")
	show, _ := cmd.Flags().GetString("show")

	if show != "" {
		p, ok := compliance.ConfigcrateProvisions[show]
		if !ok {
			return fmt.Errorf("unknown provision %q (run `vigocli compliance provisions` to list)", show)
		}
		fmt.Printf("%s %s\n", bold(p.ID), dim("("+p.Title+")"))
		fmt.Println()
		fmt.Println(wrapText(p.Description, 78, "  "))
		fmt.Println()
		fwNames := make([]string, 0, len(p.Frameworks))
		for fw := range p.Frameworks {
			fwNames = append(fwNames, fw)
		}
		sort.Strings(fwNames)
		for _, fw := range fwNames {
			fmt.Printf("  %s  %s\n", bold(fw+":"), strings.Join(p.Frameworks[fw], ", "))
		}
		return nil
	}

	ids := compliance.KnownProvisionIDs()
	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "%s\t%s\t%s\n", header("PROVISION"), header("TITLE"), header("FRAMEWORKS"))

	shown := 0
	for _, id := range ids {
		p := compliance.ConfigcrateProvisions[id]

		if framework != "" {
			if _, ok := p.Frameworks[framework]; !ok {
				continue
			}
		}
		if control != "" {
			hit := false
			for _, controls := range p.Frameworks {
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

		fwNames := make([]string, 0, len(p.Frameworks))
		for fw := range p.Frameworks {
			fwNames = append(fwNames, fw)
		}
		sort.Strings(fwNames)
		fmt.Fprintf(w, "%s\t%s\t%s\n", p.ID, p.Title, dim(strings.Join(fwNames, ", ")))
		shown++
	}

	if shown == 0 {
		fmt.Println("No provisions matched the filters.")
		return nil
	}
	return w.Flush()
}

// wrapText wraps s to width columns, prefixing each line with prefix.
// Simple word-wrap; doesn't try to handle existing newlines or
// hyphenation — descriptions in the catalog are single paragraphs.
func wrapText(s string, width int, prefix string) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(prefix)
	col := len(prefix)
	for i, w := range words {
		if i > 0 {
			if col+1+len(w) > width {
				b.WriteByte('\n')
				b.WriteString(prefix)
				col = len(prefix)
			} else {
				b.WriteByte(' ')
				col++
			}
		}
		b.WriteString(w)
		col += len(w)
	}
	return b.String()
}

func init() {
	complianceProvisionsCmd.Flags().String("framework", "", "Filter to provisions that cover this framework slug (e.g. pci-dss, nist-800-53)")
	complianceProvisionsCmd.Flags().String("control", "", "Filter to provisions that cover this control ID (e.g. 10.6.1, AU-8)")
	complianceProvisionsCmd.Flags().String("show", "", "Show the full framework->controls map for one provision")
	complianceCmd.AddCommand(complianceProvisionsCmd)
}
