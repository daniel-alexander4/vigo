// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var configHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "List recent config publishes",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Publishes []struct {
				ID           string   `json:"id"`
				Operator     string   `json:"operator"`
				PublishedAt  string   `json:"published_at"`
				ChangedFiles []string `json:"changed_files"`
				RemovedFiles []string `json:"removed_files"`
			} `json:"publishes"`
		}
		if err := client.get("/api/v1/config/publishes?limit=20", &resp); err != nil {
			return err
		}

		if len(resp.Publishes) == 0 {
			fmt.Println("No config publishes found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tOPERATOR\tPUBLISHED AT\tCHANGED\tREMOVED")
		for _, p := range resp.Publishes {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\n",
				p.ID, p.Operator, formatTimestamp(p.PublishedAt),
				len(p.ChangedFiles), len(p.RemovedFiles))
		}
		return w.Flush()
	},
}

var configHistoryShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show details for a config publish",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Publish struct {
				ID           string   `json:"id"`
				Operator     string   `json:"operator"`
				PublishedAt  string   `json:"published_at"`
				ChangedFiles []string `json:"changed_files"`
				RemovedFiles []string `json:"removed_files"`
				DiffSummary  string   `json:"diff_summary"`
			} `json:"publish"`
		}
		if err := client.get("/api/v1/config/publishes/"+args[0], &resp); err != nil {
			return err
		}

		p := resp.Publish
		fmt.Printf("ID:          %s\n", p.ID)
		fmt.Printf("Operator:    %s\n", p.Operator)
		fmt.Printf("Published:   %s\n", formatTimestamp(p.PublishedAt))

		if len(p.ChangedFiles) > 0 {
			fmt.Println("Changed files:")
			for _, f := range p.ChangedFiles {
				fmt.Printf("  + %s\n", f)
			}
		}
		if len(p.RemovedFiles) > 0 {
			fmt.Println("Removed files:")
			for _, f := range p.RemovedFiles {
				fmt.Printf("  - %s\n", f)
			}
		}
		if p.DiffSummary != "" {
			fmt.Println("\nDiff:")
			fmt.Println(p.DiffSummary)
		}
		return nil
	},
}

var _ = json.Marshal // used by confighistory

func init() {
	configHistoryCmd.AddCommand(configHistoryShowCmd)
	configCmd.AddCommand(configHistoryCmd)
}
