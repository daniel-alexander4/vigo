// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var rollbackCmd = &cobra.Command{
	Use:   "rollback <hostname-or-uuid>",
	Short: "Revert a envoy's file resources to a previous convergence run's state",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		runID, _ := cmd.Flags().GetString("run")
		filePath, _ := cmd.Flags().GetString("file")

		if runID == "" && filePath == "" {
			return fmt.Errorf("specify --run <run-uuid> or --file <path> (exactly one required)")
		}
		if runID != "" && filePath != "" {
			return fmt.Errorf("specify only one of --run or --file, not both")
		}

		if !confirmAction("Roll back this envoy's file resources to the previous state?") {
			fmt.Println("Cancelled.")
			return nil
		}

		client := newAPIClient()
		body := map[string]string{
			"envoy_id": args[0],
		}
		if runID != "" {
			body["run_id"] = runID
		}
		if filePath != "" {
			body["file_path"] = filePath
		}

		var resp struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
		}
		err := client.post("/api/v1/envoys/"+args[0]+"/rollback", body, &resp)
		if err != nil {
			return err
		}

		if resp.Success {
			fmt.Printf("Rollback successful: %s\n", resp.Message)
		} else {
			fmt.Printf("Rollback failed: %s\n", resp.Message)
		}
		return nil
	},
}

func init() {
	rollbackCmd.Flags().String("run", "", "Run UUID to rollback to")
	rollbackCmd.Flags().String("file", "", "File path to rollback")
}
