// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

var configRollbackCmd = &cobra.Command{
	Use:   "rollback [version]",
	Short: "Restore .live/ config to a previous timestamped snapshot",
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
				fmt.Println(dim("No config snapshots available."))
				return nil
			}
			fmt.Println(bold("Available versions:"))
			for _, v := range resp.Versions {
				fmt.Printf("  %s\n", dim(v))
			}
			return nil
		}

		version := args[0]
		ok, err := confirmAction(cmd, "Roll back config to this version? This affects all envoys.")
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("Cancelled.")
			return nil
		}

		if err := client.post("/api/v1/config/rollback/"+url.PathEscape(version), nil, nil); err != nil {
			return err
		}
		fmt.Printf("%s Rolled back to version %s\n", green("OK"), dim(version))
		return nil
	},
}

func init() {
	configCmd.AddCommand(configRollbackCmd)
}
