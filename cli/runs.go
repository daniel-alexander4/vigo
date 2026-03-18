// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var runsCmd = &cobra.Command{
	Use:   "runs",
	Short: "View convergence run history showing what changed on each thrall",
}

var runsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent convergence runs with status and resource change counts",
	RunE: func(cmd *cobra.Command, args []string) error {
		thrallID, _ := cmd.Flags().GetString("thrall")
		limit, _ := cmd.Flags().GetInt("limit")

		path := "/api/v1/runs"
		sep := "?"
		if thrallID != "" {
			path += fmt.Sprintf("%sthrall_id=%s", sep, thrallID)
			sep = "&"
		}
		if limit > 0 {
			path += fmt.Sprintf("%slimit=%d", sep, limit)
		}

		client := newAPIClient()
		var resp struct {
			Runs []struct {
				ID           string `json:"id"`
				ThrallID       string `json:"thrallId"`
				StartedAt    string `json:"startedAt"`
				Status       string `json:"status"`
				ChangedCount int32  `json:"changedCount"`
				FailedCount  int32  `json:"failedCount"`
			} `json:"runs"`
		}
		if err := client.get(path, &resp); err != nil {
			return err
		}

		if len(resp.Runs) == 0 {
			fmt.Println("No runs found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", header("ID"), header("THRALL"), header("STARTED"), header("STATUS"), header("CHANGED"), header("FAILED"))
		for _, r := range resp.Runs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\n",
				dim(r.ID), r.ThrallID,
				dim(formatTimestamp(r.StartedAt)),
				colorStatus(r.Status), r.ChangedCount, r.FailedCount)
		}
		return w.Flush()
	},
}

var runsShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show per-resource outcomes for a specific convergence run",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Run struct {
				ID           string `json:"id"`
				ThrallID       string `json:"thrallId"`
				StartedAt    string `json:"startedAt"`
				FinishedAt   string `json:"finishedAt"`
				Status       string `json:"status"`
				ModuleCount  int32  `json:"moduleCount"`
				ChangedCount int32  `json:"changedCount"`
				FailedCount  int32  `json:"failedCount"`
				SkippedCount int32  `json:"skippedCount"`
			} `json:"run"`
			Results []struct {
				ModuleName   string `json:"moduleName"`
				ResourceType string `json:"resourceType"`
				ResourceName string `json:"resourceName"`
				ActionTaken  string `json:"actionTaken"`
				Changed      bool   `json:"changed"`
				ErrorMessage string `json:"errorMessage"`
				DurationMs   int32  `json:"durationMs"`
			} `json:"results"`
		}
		if err := client.get("/api/v1/runs/"+args[0], &resp); err != nil {
			return err
		}

		r := resp.Run
		fmt.Printf("Run:      %s\n", r.ID)
		fmt.Printf("Thrall:     %s\n", r.ThrallID)
		fmt.Printf("Started:  %s\n", formatTimestamp(r.StartedAt))
		fmt.Printf("Finished: %s\n", formatTimestamp(r.FinishedAt))
		fmt.Printf("Status:   %s\n", colorStatus(r.Status))
		fmt.Printf("Modules:  %d total, %s changed, %s failed, %s skipped\n",
			r.ModuleCount, yellow(fmt.Sprintf("%d", r.ChangedCount)), red(fmt.Sprintf("%d", r.FailedCount)), dim(fmt.Sprintf("%d", r.SkippedCount)))

		if len(resp.Results) > 0 {
			fmt.Println()
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", header("MODULE"), header("TYPE"), header("RESOURCE"), header("ACTION"), header("CHANGED"), header("ERROR"))
			for _, res := range resp.Results {
				errStr := dim("-")
				if res.ErrorMessage != "" {
					errStr = red(res.ErrorMessage)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\t%s\n",
					res.ModuleName, dim(res.ResourceType), res.ResourceName,
					colorAction(res.ActionTaken), res.Changed, errStr)
			}
			w.Flush()
		}

		return nil
	},
}

func init() {
	runsListCmd.Flags().String("thrall", "", "Filter by thrall ID")
	runsListCmd.Flags().Int("limit", 20, "Maximum number of runs to display")
	runsCmd.AddCommand(runsListCmd, runsShowCmd)
}
