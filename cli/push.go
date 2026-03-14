// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var pushCmd = &cobra.Command{
	Use:   "push [hostname-or-uuid]",
	Short: "Trigger an immediate check-in for a thrall or all thralls",
	RunE: func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")
		thrallID := ""
		if !all {
			if len(args) < 1 {
				return fmt.Errorf("specify a hostname or UUID, or use --all")
			}
			thrallID = args[0]
		}

		client := newAPIClient()
		var resp struct {
			ThrallsNotified int32 `json:"thrallsNotified"`
		}
		err := client.post("/api/v1/push", map[string]string{
			"thrall_id": thrallID,
		}, &resp)
		if err != nil {
			return err
		}

		fmt.Printf("Force push sent — %d thrall(s) notified\n", resp.ThrallsNotified)
		return nil
	},
}

func init() {
	pushCmd.Flags().Bool("all", false, "Push to all thralls")
}
