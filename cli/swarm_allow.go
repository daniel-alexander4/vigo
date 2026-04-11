// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var swarmAllowSeedCmd = &cobra.Command{
	Use:   "allow-seed",
	Short: "Manage hostname patterns that control which envoys can seed swarm files",
}

var swarmAllowSeedAddCmd = &cobra.Command{
	Use:   "add <pattern>",
	Short: "Allow envoys matching a hostname glob to seed swarm files",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			ID      string `json:"id"`
			Pattern string `json:"pattern"`
		}
		err := client.post("/api/v1/swarm/seeders", map[string]string{
			"pattern": args[0],
		}, &resp)
		if err != nil {
			return err
		}
		fmt.Printf("  %s  Added seeder pattern: %s\n", green("✓"), resp.Pattern)
		return nil
	},
}

var swarmAllowSeedRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove a seed allowlist pattern",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !confirmAction("Remove this seeder pattern?") {
			fmt.Println("Cancelled.")
			return nil
		}
		client := newAPIClient()
		if err := client.delete("/api/v1/swarm/seeders/"+args[0], nil); err != nil {
			return err
		}
		fmt.Printf("  %s  Removed seeder pattern %s\n", green("✓"), args[0])
		return nil
	},
}

var swarmAllowSeedListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show all seed allowlist patterns",
	RunE:  runSwarmAllowSeedList,
}

func runSwarmAllowSeedList(cmd *cobra.Command, args []string) error {
	client := newAPIClient()
	var entries []struct {
		ID        string `json:"id"`
		Pattern   string `json:"pattern"`
		CreatedAt string `json:"created_at"`
		CreatedBy string `json:"created_by"`
	}
	if err := client.get("/api/v1/swarm/seeders", &entries); err != nil {
		return err
	}

	if len(entries) == 0 {
		fmt.Println(dim("No seed allowlist patterns configured."))
		fmt.Println(dim("Envoys can only seed when their hostname matches an allowlist pattern."))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", header("ID"), header("PATTERN"), header("CREATED"), header("BY"))
	for _, e := range entries {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", dim(e.ID), e.Pattern, dim(formatTimestamp(e.CreatedAt)), dim(e.CreatedBy))
	}
	return w.Flush()
}

func init() {
	swarmAllowSeedCmd.AddCommand(swarmAllowSeedAddCmd, swarmAllowSeedRemoveCmd, swarmAllowSeedListCmd)
}
