// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var webhooksCmd = &cobra.Command{
	Use:   "webhooks",
	Short: "Manage HMAC-signed HTTP webhook endpoints for event notifications",
}

var webhooksAddCmd = &cobra.Command{
	Use:   "add <url>",
	Short: "Register a URL to receive HMAC-signed event notifications (runs, drift, secrets)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		events, _ := cmd.Flags().GetString("events")
		secret, _ := cmd.Flags().GetString("secret")
		if secret == "" {
			return fmt.Errorf("--secret is required for HMAC signing")
		}

		eventList := strings.Split(events, ",")
		for i := range eventList {
			eventList[i] = strings.TrimSpace(eventList[i])
		}

		client := newAPIClient()
		var resp struct {
			Webhook struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"webhook"`
		}
		err := client.post("/api/v1/webhooks", map[string]any{
			"url":    args[0],
			"secret": secret,
			"events": eventList,
		}, &resp)
		if err != nil {
			return err
		}

		fmt.Printf("Created webhook %s -> %s\n", resp.Webhook.ID, resp.Webhook.URL)
		return nil
	},
}

var webhooksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all registered webhook endpoints with their subscribed events",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Webhooks []struct {
				ID     string   `json:"id"`
				URL    string   `json:"url"`
				Events []string `json:"events"`
				Active bool     `json:"active"`
			} `json:"webhooks"`
		}
		if err := client.get("/api/v1/webhooks", &resp); err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tURL\tEVENTS\tACTIVE")
		for _, wh := range resp.Webhooks {
			fmt.Fprintf(w, "%s\t%s\t%s\t%v\n",
				wh.ID, wh.URL, strings.Join(wh.Events, ","), wh.Active)
		}
		return w.Flush()
	},
}

var webhooksDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Unregister a webhook endpoint so it stops receiving events",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !confirmAction("Delete this webhook endpoint?") {
			fmt.Println("Cancelled.")
			return nil
		}

		client := newAPIClient()
		if err := client.delete("/api/v1/webhooks/"+args[0], nil); err != nil {
			return err
		}
		fmt.Printf("Deleted webhook %s\n", args[0])
		return nil
	},
}

func init() {
	webhooksAddCmd.Flags().String("events", "run.failure,drift.detected", "Comma-separated list of event types")
	webhooksAddCmd.Flags().String("secret", "", "HMAC secret for webhook signing")
	webhooksCmd.AddCommand(webhooksAddCmd, webhooksListCmd, webhooksDeleteCmd)
}
