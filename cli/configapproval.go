// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var configPendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "List config changes awaiting approval",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Changes []struct {
				ID          int64  `json:"id"`
				Status      string `json:"status"`
				RequestedBy string `json:"requested_by"`
				RequestedAt string `json:"requested_at"`
				ExpiresAt   string `json:"expires_at"`
				DiffSummary string `json:"diff_summary"`
			} `json:"changes"`
		}
		if err := client.get("/api/v1/config/changes", &resp); err != nil {
			return err
		}

		if len(resp.Changes) == 0 {
			fmt.Println("No pending config changes.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", header("ID"), header("STATUS"), header("REQUESTED BY"), header("REQUESTED AT"), header("EXPIRES AT"))
		for _, c := range resp.Changes {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n",
				c.ID, colorStatus(c.Status), c.RequestedBy,
				dim(formatTimestamp(c.RequestedAt)),
				dim(formatTimestamp(c.ExpiresAt)))
		}
		return w.Flush()
	},
}

var configApproveCmd = &cobra.Command{
	Use:   "approve <id>",
	Short: "Approve a pending config change and trigger config reload",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Status      string `json:"status"`
			ChangeID    int64  `json:"change_id"`
			RequestedBy string `json:"requested_by"`
			ApprovedBy  string `json:"approved_by"`
		}
		if err := client.post("/api/v1/config/changes/"+args[0]+"/approve", nil, &resp); err != nil {
			return err
		}
		fmt.Printf("Change %d approved by %s (requested by %s) — config reloaded.\n",
			resp.ChangeID, resp.ApprovedBy, resp.RequestedBy)
		return nil
	},
}

var configRejectCmd = &cobra.Command{
	Use:   "reject <id>",
	Short: "Reject a pending config change",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		reason, _ := cmd.Flags().GetString("reason")
		client := newAPIClient()
		body := map[string]string{}
		if reason != "" {
			body["reason"] = reason
		}
		if err := client.post("/api/v1/config/changes/"+args[0]+"/reject", body, nil); err != nil {
			return err
		}
		fmt.Printf("Change %s rejected.\n", args[0])
		return nil
	},
}

func init() {
	configRejectCmd.Flags().String("reason", "", "reason for rejection")
	configCmd.AddCommand(configPendingCmd, configApproveCmd, configRejectCmd)
}
