// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var federationCmd = &cobra.Command{
	Use:   "federation",
	Short: "Manage and inspect federation status",
}

var federationStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show federation role and spoke health",
	RunE:  runFederationStatus,
}

func init() {
	federationCmd.AddCommand(federationStatusCmd)
}

func runFederationStatus(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	var resp struct {
		Role    string `json:"role"`
		SpokeID string `json:"spoke_id"`
		Spokes  []struct {
			ID          string   `json:"id"`
			Addr        string   `json:"addr"`
			Patterns    []string `json:"patterns"`
			Healthy     bool     `json:"healthy"`
			ThrallCount int32    `json:"thrall_count"`
			Error       string   `json:"error"`
		} `json:"spokes"`
	}

	if err := client.get("/api/v1/federation/status", &resp); err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}

	fmt.Printf("  Role: %s%s%s\n", colorBold, resp.Role, colorReset)

	if resp.SpokeID != "" {
		fmt.Printf("  Spoke ID: %s\n", resp.SpokeID)
	}

	if resp.Role == "standalone" {
		fmt.Println("  No federation configured.")
		return nil
	}

	if len(resp.Spokes) == 0 {
		return nil
	}

	// Count healthy/total
	healthy := 0
	var totalThralls int32
	for _, s := range resp.Spokes {
		if s.Healthy {
			healthy++
		}
		totalThralls += s.ThrallCount
	}

	fmt.Printf("  Spokes: %d (%d healthy)\n", len(resp.Spokes), healthy)
	fmt.Printf("  Total Thralls: %d\n\n", totalThralls)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  ID\tADDRESS\tTHRALLS\tPATTERNS\tSTATUS")
	for _, s := range resp.Spokes {
		status := colorGreen + "healthy" + colorReset
		if !s.Healthy {
			status = colorRed + "unreachable" + colorReset
		}
		patterns := strings.Join(s.Patterns, ", ")
		fmt.Fprintf(w, "  %s\t%s\t%d\t%s\t%s\n", s.ID, s.Addr, s.ThrallCount, patterns, status)
	}
	w.Flush()

	return nil
}

const colorBold = "\033[1m"
