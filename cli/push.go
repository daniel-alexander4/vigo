// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var envoysPushCmd = &cobra.Command{
	Use:               "push [hostname-or-uuid]",
	Short:             "Force targeted envoys to check in and converge immediately instead of waiting for their next interval",
	ValidArgsFunction: completeEnvoyIDs,
	RunE: func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")
		if !all && len(args) < 1 {
			return fmt.Errorf("specify a hostname or UUID, or use --all")
		}

		client := newAPIClient()

		if all {
			if !confirmAction("Force check-in for ALL envoys?") {
				fmt.Println("Cancelled.")
				return nil
			}
			var resp struct {
				Status string `json:"status"`
				Count  int    `json:"count"`
			}
			if err := client.post("/api/v1/envoys/update", nil, &resp); err != nil {
				return err
			}
			fmt.Printf("%s Force update queued for %d envoy(s)\n", green("OK"), resp.Count)
			return nil
		}

		envoyID, err := resolveEnvoy(client, args[0])
		if err != nil {
			return err
		}

		var resp struct {
			Status string `json:"status"`
		}
		if err := client.post("/api/v1/envoys/"+envoyID+"/update", nil, &resp); err != nil {
			return err
		}
		fmt.Printf("%s Force update queued for %s\n", green("OK"), args[0])
		return nil
	},
}

func init() {
	envoysPushCmd.Flags().Bool("all", false, "Push to all envoys")
}
