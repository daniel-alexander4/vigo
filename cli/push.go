// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var envoysPushCmd = &cobra.Command{
	Use:   "push [hostname-or-uuid]",
	Short: "Flag envoys to re-download the agent binary and restart at their next check-in",
	Long: `Flag one envoy, or every active envoy with --all, to download the latest agent
binary and restart at its next check-in. It does not make the envoy check in
sooner: config you publish reaches each envoy at its next scheduled check-in
(checkin.interval in server.yaml). This sets the same flag as
"vigocli envoys rebootstrap".`,
	ValidArgsFunction: completeEnvoyIDs,
	RunE: func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")
		if !all && len(args) < 1 {
			return fmt.Errorf("specify a hostname or UUID, or use --all")
		}

		client := newAPIClient()

		if all {
			ok, err := confirmAction(cmd, "Flag ALL envoys to re-download the agent and restart?")
			if err != nil {
				return err
			}
			if !ok {
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
	envoysPushCmd.Flags().Bool("all", false, "Flag all active envoys")
}
