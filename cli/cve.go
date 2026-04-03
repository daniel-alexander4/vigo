// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var cveCmd = &cobra.Command{
	Use:   "cve <CVE-ID>",
	Short: "Search the fleet for hosts affected by a specific CVE",
	Long: `Searches all envoy security scan traits for the given CVE ID and
returns affected hosts with package name, installed version, fixed
version (if available), severity, and scanner source.

Example:
  vigocli cve CVE-2024-1234`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		cveID := strings.ToUpper(args[0])

		var result struct {
			CVEID         string `json:"cve_id"`
			AffectedHosts []struct {
				Hostname  string `json:"hostname"`
				EnvoyID   string `json:"envoy_id"`
				Package   string `json:"package"`
				Installed string `json:"installed"`
				Fixed     string `json:"fixed"`
				Severity  string `json:"severity"`
				Scanner   string `json:"scanner"`
			} `json:"affected_hosts"`
			TotalHosts int `json:"total_hosts"`
		}

		if err := client.get("/api/v1/cve/"+url.PathEscape(cveID), &result); err != nil {
			return err
		}

		if result.TotalHosts == 0 {
			fmt.Printf("  No hosts affected by %s\n", cveID)
			return nil
		}

		fmt.Printf("  %s — %d host(s) affected\n\n", bold(cveID), result.TotalHosts)

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
			header("HOSTNAME"), header("PACKAGE"), header("INSTALLED"), header("FIXED"), header("SEVERITY"), header("SCANNER"))
		for _, h := range result.AffectedHosts {
			fixed := h.Fixed
			if fixed == "" {
				fixed = "-"
			}
			sev := h.Severity
			switch strings.ToLower(sev) {
			case "critical":
				sev = red(sev)
			case "high":
				sev = amber(sev)
			case "medium":
				sev = yellow(sev)
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n",
				h.Hostname, h.Package, h.Installed, fixed, sev, h.Scanner)
		}
		w.Flush()
		return nil
	},
}

func init() {
	securityCmd.AddCommand(cveCmd)
}
