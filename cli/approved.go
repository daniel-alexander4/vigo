// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var approvedCmd = &cobra.Command{
	Use:   "approved",
	Short: "Manage hostname patterns that control which machines can enroll",
}

var approvedAddCmd = &cobra.Command{
	Use:   "add <pattern>",
	Short: "Allow machines matching a hostname glob to enroll using a one-time token",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		token, _ := cmd.Flags().GetString("token")
		if token == "" {
			return fmt.Errorf("--token is required")
		}

		client := newAPIClient()
		var resp struct {
			ID      string `json:"id"`
			Pattern string `json:"pattern"`
		}
		err := client.post("/api/v1/approved", map[string]string{
			"pattern": args[0],
			"token":   token,
		}, &resp)
		if err != nil {
			return err
		}

		fmt.Printf("Created approved pattern %s (ID: %s)\n", resp.Pattern, resp.ID)
		return nil
	},
}

var approvedListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show all enrollment patterns and whether their token has been consumed",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Patterns []struct {
				ID        string `json:"id"`
				Pattern   string `json:"pattern"`
				CreatedAt string `json:"createdAt"`
				Used      bool   `json:"used"`
				UsedBy    string `json:"usedBy"`
			} `json:"patterns"`
		}
		if err := client.get("/api/v1/approved", &resp); err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tPATTERN\tCREATED\tUSED")
		for _, p := range resp.Patterns {
			used := "no"
			if p.Used {
				used = fmt.Sprintf("yes (%s)", p.UsedBy)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.ID, p.Pattern, formatTimestamp(p.CreatedAt), used)
		}
		return w.Flush()
	},
}

var approvedRevokeCmd = &cobra.Command{
	Use:   "revoke <id>",
	Short: "Delete an enrollment pattern so no new machines can match it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !confirmAction("Revoke this enrollment pattern? New machines matching it cannot enroll.") {
			fmt.Println("Cancelled.")
			return nil
		}

		client := newAPIClient()
		if err := client.delete("/api/v1/approved/"+args[0], nil); err != nil {
			return err
		}
		fmt.Printf("Revoked approved pattern %s\n", args[0])
		return nil
	},
}

func init() {
	approvedAddCmd.Flags().String("token", "", "One-time enrollment token")
	approvedCmd.AddCommand(approvedAddCmd, approvedListCmd, approvedRevokeCmd, tokensGenerateCmd)
}
