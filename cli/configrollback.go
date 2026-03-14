// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var configRollbackCmd = &cobra.Command{
	Use:   "rollback [version]",
	Short: "Rollback config to a previous version",
	Long:  "Lists available versions if no argument given. Rolls back to the specified version otherwise.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()

		if len(args) == 0 {
			var resp struct {
				Versions []string `json:"versions"`
			}
			if err := client.get("/api/v1/config/versions", &resp); err != nil {
				return err
			}
			if len(resp.Versions) == 0 {
				fmt.Println("No config snapshots available.")
				return nil
			}
			fmt.Println("Available versions:")
			for _, v := range resp.Versions {
				fmt.Printf("  %s\n", v)
			}
			return nil
		}

		version := args[0]
		if err := client.post("/api/v1/config/rollback/"+version, nil, nil); err != nil {
			return err
		}
		fmt.Printf("Rolled back to version %s\n", version)
		return nil
	},
}

func init() {
	configCmd.AddCommand(configRollbackCmd)
}
