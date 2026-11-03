// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "View the unified audit trail of config publishes, task dispatches, and workflows",
}

var auditListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent audit events filtered by type, operator, or time range",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()

		since, _ := cmd.Flags().GetString("since")
		operator, _ := cmd.Flags().GetString("operator")
		eventType, _ := cmd.Flags().GetString("type")
		limit, _ := cmd.Flags().GetInt("limit")

		params := url.Values{}
		params.Set("limit", fmt.Sprintf("%d", limit))
		if since != "" {
			params.Set("since", since)
		}
		if operator != "" {
			params.Set("operator", operator)
		}
		if eventType != "" {
			params.Set("type", eventType)
		}
		path := "/api/v1/audit?" + params.Encode()

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

		w := newAlignedTable(os.Stdout)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", header("TIMESTAMP"), header("TYPE"), header("OPERATOR"), header("SUMMARY"))
		for _, e := range resp.Events {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				dim(formatTimestamp(e.Timestamp)), e.Type, e.Operator, e.Summary)
		}
		return w.Flush()
	},
}

var auditVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify the integrity of the tamper-evident audit chain",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()

		var resp struct {
			Valid       bool   `json:"valid"`
			EntryCount  int64  `json:"entry_count"`
			FirstBroken int64  `json:"first_broken"`
			Error       string `json:"error"`
		}
		if err := client.get("/api/v1/audit/verify", &resp); err != nil {
			return err
		}

		if resp.Valid {
			fmt.Printf("Audit chain: valid (%d entries)\n", resp.EntryCount)
		} else {
			fmt.Printf("Audit chain: BROKEN at entry #%d -- %s\n", resp.FirstBroken, resp.Error)
			os.Exit(1)
		}
		return nil
	},
}

func init() {
	auditListCmd.Flags().String("since", "", "time filter (duration like 24h or RFC3339)")
	auditListCmd.Flags().String("operator", "", "filter by operator")
	auditListCmd.Flags().String("type", "", "filter by event type (config_publish, task_run, workflow_run, audit_entry)")
	auditListCmd.Flags().Int("limit", 50, "maximum events to return")

	auditCmd.AddCommand(auditListCmd)
	auditCmd.AddCommand(auditVerifyCmd)
	securityCmd.AddCommand(auditCmd)
}
