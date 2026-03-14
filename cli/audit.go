// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "View audit log",
}

var auditListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent audit events",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()

		since, _ := cmd.Flags().GetString("since")
		operator, _ := cmd.Flags().GetString("operator")
		eventType, _ := cmd.Flags().GetString("type")
		limit, _ := cmd.Flags().GetInt("limit")

		path := fmt.Sprintf("/api/v1/audit?limit=%d", limit)
		if since != "" {
			path += "&since=" + since
		}
		if operator != "" {
			path += "&operator=" + operator
		}
		if eventType != "" {
			path += "&type=" + eventType
		}

		var resp struct {
			Events []struct {
				ID        string `json:"id"`
				Type      string `json:"type"`
				Operator  string `json:"operator"`
				Timestamp string `json:"timestamp"`
				Summary   string `json:"summary"`
			} `json:"events"`
		}
		if err := client.get(path, &resp); err != nil {
			return err
		}

		if len(resp.Events) == 0 {
			fmt.Println("No audit events found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "TIMESTAMP\tTYPE\tOPERATOR\tSUMMARY")
		for _, e := range resp.Events {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				formatTimestamp(e.Timestamp), e.Type, e.Operator, e.Summary)
		}
		return w.Flush()
	},
}

func init() {
	auditListCmd.Flags().String("since", "", "time filter (duration like 24h or RFC3339)")
	auditListCmd.Flags().String("operator", "", "filter by operator")
	auditListCmd.Flags().String("type", "", "filter by event type (config_publish, task_run, workflow_run)")
	auditListCmd.Flags().Int("limit", 50, "maximum events to return")

	auditCmd.AddCommand(auditListCmd)
	rootCmd.AddCommand(auditCmd)
}
