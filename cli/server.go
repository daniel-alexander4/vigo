// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Manage peer replication: promote a server to primary, check replication status",
}

var serverPromoteCmd = &cobra.Command{
	Use:   "promote",
	Short: "Promote the target server to primary role",
	Long: `Promote a peer server to primary role for peer replication.

By default, promotes the server at the configured --server address.
Use --target to promote a different server via its REST API.`,
	RunE: runServerPromote,
}

var serverStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show peer replication status: role, primary address, sync state, and config version",
	RunE:  runServerStatus,
}

func init() {
	serverPromoteCmd.Flags().String("target", "", "REST API address of the server to promote (default: --server value)")
	serverYamlCmd.AddCommand(serverYamlFullCmd)
	serverYamlCmd.AddCommand(serverYamlListCmd)

	serverCmd.AddCommand(serverPromoteCmd)
	serverCmd.AddCommand(serverStatusCmd)
	configCmd.AddCommand(serverYamlCmd)
}

func runServerPromote(cmd *cobra.Command, args []string) error {
	target, _ := cmd.Flags().GetString("target")

	var client *apiClient
	if target != "" {
		// Override the server URL for this command.
		origURL := serverURL
		serverURL = target
		client = newAPIClient()
		serverURL = origURL
	} else {
		client = newAPIClient()
	}

	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}

	if err := client.post("/api/v1/peer/promote", nil, &resp); err != nil {
		return fmt.Errorf("promote failed: %w", err)
	}

	if resp.Success {
		fmt.Printf("  %s\n", green("Promoted to primary."))
	} else {
		fmt.Printf("  %s %s\n", red("Promotion failed:"), resp.Message)
	}

	if resp.Message != "" {
		fmt.Printf("  %s\n", resp.Message)
	}

	return nil
}

func runServerStatus(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	var resp struct {
		Role          string `json:"role"`
		PrimaryAddr   string `json:"primary_addr"`
		LastSyncTime  string `json:"last_sync_time"`
		FilesSynced   int64  `json:"files_synced"`
		ConfigVersion uint64 `json:"config_version"`
	}

	if err := client.get("/api/v1/peer/status", &resp); err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}

	roleStr := green(resp.Role)
	if resp.Role == "peer" {
		roleStr = yellow(resp.Role)
	}

	fmt.Printf("  Role:           %s\n", roleStr)

	if resp.PrimaryAddr != "" {
		fmt.Printf("  Primary:        %s\n", resp.PrimaryAddr)
	}

	if resp.LastSyncTime != "" {
		fmt.Printf("  Last sync:      %s\n", resp.LastSyncTime)
	} else {
		fmt.Printf("  Last sync:      never\n")
	}

	fmt.Printf("  Files synced:   %d\n", resp.FilesSynced)
	fmt.Printf("  Config version: %d\n", resp.ConfigVersion)

	return nil
}
