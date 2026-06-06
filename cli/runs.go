// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var runsCmd = &cobra.Command{
	Use:   "runs",
	Short: "View convergence run history showing what changed on each envoy",
}

var runsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent convergence runs with status and resource change counts",
	RunE: func(cmd *cobra.Command, args []string) error {
		envoyID, _ := cmd.Flags().GetString("envoy")
		limit, _ := cmd.Flags().GetInt("limit")

		path := "/api/v1/runs"
		sep := "?"
		if envoyID != "" {
			path += fmt.Sprintf("%senvoy_id=%s", sep, envoyID)
			sep = "&"
		}
		if limit > 0 {
			path += fmt.Sprintf("%slimit=%d", sep, limit)
		}

		client := newAPIClient()
		var resp struct {
			Runs []struct {
				ID           string `json:"id"`
				EnvoyID      string `json:"envoyId"`
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

		w := newAlignedTable(os.Stdout)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", header("ID"), header("ENVOY"), header("STARTED"), header("STATUS"), header("CHANGED"), header("FAILED"))
		for _, r := range resp.Runs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\n",
				dim(r.ID), r.EnvoyID,
				dim(formatTimestamp(r.StartedAt)),
				colorStatus(r.Status), r.ChangedCount, r.FailedCount)
		}
		return w.Flush()
	},
}

var runsShowCmd = &cobra.Command{
	Use:               "show <id>",
	Short:             "Show per-resource outcomes for a specific convergence run",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeRunIDs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Run struct {
				ID            string `json:"id"`
				EnvoyID       string `json:"envoyId"`
				StartedAt     string `json:"startedAt"`
				FinishedAt    string `json:"finishedAt"`
				Status        string `json:"status"`
				ResourceCount int32  `json:"moduleCount"`
				ChangedCount  int32  `json:"changedCount"`
				FailedCount   int32  `json:"failedCount"`
				SkippedCount  int32  `json:"skippedCount"`
			} `json:"run"`
			Results []struct {
				ConfigcrateName string `json:"moduleName"`
				ResourceType    string `json:"resourceType"`
				ResourceName    string `json:"resourceName"`
				ActionTaken     string `json:"actionTaken"`
				Changed         bool   `json:"changed"`
				ErrorMessage    string `json:"errorMessage"`
				DurationMs      int32  `json:"durationMs"`
			} `json:"results"`
		}
		if err := client.get("/api/v1/runs/"+args[0], &resp); err != nil {
			return err
		}

		r := resp.Run
		fmt.Printf("Run:      %s\n", r.ID)
		fmt.Printf("Envoy:     %s\n", r.EnvoyID)
		fmt.Printf("Started:  %s\n", formatTimestamp(r.StartedAt))
		fmt.Printf("Finished: %s\n", formatTimestamp(r.FinishedAt))
		fmt.Printf("Status:   %s\n", colorStatus(r.Status))
		fmt.Printf("Configcrates:  %d total, %s changed, %s failed, %s skipped\n",
			r.ResourceCount, yellow(fmt.Sprintf("%d", r.ChangedCount)), red(fmt.Sprintf("%d", r.FailedCount)), dim(fmt.Sprintf("%d", r.SkippedCount)))

		if len(resp.Results) > 0 {
			fmt.Println()
			w := newAlignedTable(os.Stdout)
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", header("CONFIGCRATE"), header("TYPE"), header("RESOURCE"), header("ACTION"), header("CHANGED"), header("ERROR"))
			for _, res := range resp.Results {
				errStr := dim("-")
				if res.ErrorMessage != "" {
					errStr = red(res.ErrorMessage)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\t%s\n",
					res.ConfigcrateName, dim(res.ResourceType), res.ResourceName,
					colorAction(res.ActionTaken), res.Changed, errStr)
			}
			w.Flush()
		}

		return nil
	},
}

var runsDriftCmd = &cobra.Command{
	Use:   "drift",
	Short: "Show resources causing drift or errors on each envoy's most recent run",
	Example: `  vigocli runs drift
  vigocli runs drift --envoy web01.example.com`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		envoyFlag, _ := cmd.Flags().GetString("envoy")

		// Build list of (envoyID, hostname) pairs to check.
		type target struct{ id, hostname string }
		var targets []target

		if envoyFlag != "" {
			id, err := resolveEnvoy(client, envoyFlag)
			if err != nil {
				return err
			}
			targets = append(targets, target{id, envoyFlag})
		} else {
			envoys, err := listAllEnvoys(client, false)
			if err != nil {
				return err
			}
			for _, e := range envoys {
				if !e.Revoked {
					targets = append(targets, target{e.ID, e.Hostname})
				}
			}
		}

		if len(targets) == 0 {
			fmt.Println("No envoys found.")
			return nil
		}

		type driftRow struct {
			hostname     string
			moduleName   string
			resourceType string
			resourceName string
			actionTaken  string
			errorMessage string
		}

		var rows []driftRow
		var driftingEnvoys int

		for _, t := range targets {
			// Get latest run for this envoy.
			var listResp struct {
				Runs []struct {
					ID           string `json:"id"`
					StartedAt    string `json:"startedAt"`
					Status       string `json:"status"`
					ChangedCount int32  `json:"changedCount"`
					FailedCount  int32  `json:"failedCount"`
				} `json:"runs"`
			}
			if err := client.get(fmt.Sprintf("/api/v1/runs?envoy_id=%s&limit=1", t.id), &listResp); err != nil {
				continue
			}
			if len(listResp.Runs) == 0 {
				continue
			}
			run := listResp.Runs[0]
			if run.ChangedCount == 0 && run.FailedCount == 0 {
				continue
			}

			// Fetch per-resource results.
			var showResp struct {
				Run struct {
					StartedAt string `json:"startedAt"`
					Status    string `json:"status"`
				} `json:"run"`
				Results []struct {
					ConfigcrateName string `json:"moduleName"`
					ResourceType    string `json:"resourceType"`
					ResourceName    string `json:"resourceName"`
					ActionTaken     string `json:"actionTaken"`
					Changed         bool   `json:"changed"`
					ErrorMessage    string `json:"errorMessage"`
				} `json:"results"`
			}
			if err := client.get("/api/v1/runs/"+run.ID, &showResp); err != nil {
				continue
			}

			envoyHadDrift := false
			for _, res := range showResp.Results {
				if !res.Changed && res.ErrorMessage == "" {
					continue
				}
				envoyHadDrift = true
				rows = append(rows, driftRow{
					hostname:     t.hostname,
					moduleName:   res.ConfigcrateName,
					resourceType: res.ResourceType,
					resourceName: res.ResourceName,
					actionTaken:  res.ActionTaken,
					errorMessage: res.ErrorMessage,
				})
			}
			if envoyHadDrift {
				driftingEnvoys++
			}
		}

		if len(rows) == 0 {
			fmt.Println("All envoys converged — no drift detected.")
			return nil
		}

		w := newAlignedTable(os.Stdout)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			header("HOSTNAME"), header("CONFIGCRATE"), header("TYPE"),
			header("RESOURCE"), header("ACTION"), header("ERROR"))
		for _, r := range rows {
			errStr := dim("-")
			if r.errorMessage != "" {
				errStr = red(r.errorMessage)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				r.hostname, r.moduleName, dim(r.resourceType),
				r.resourceName, colorAction(r.actionTaken), errStr)
		}
		w.Flush()

		changed := 0
		errored := 0
		for _, r := range rows {
			if r.errorMessage != "" {
				errored++
			} else {
				changed++
			}
		}
		parts := []string{}
		if changed > 0 {
			parts = append(parts, fmt.Sprintf("%d drifting", changed))
		}
		if errored > 0 {
			parts = append(parts, fmt.Sprintf("%d errored", errored))
		}
		noun := "resource"
		if changed+errored != 1 {
			noun = "resources"
		}
		envoyNoun := "envoy"
		if driftingEnvoys != 1 {
			envoyNoun = "envoys"
		}
		fmt.Printf("\n%s %s across %d %s\n",
			strings.Join(parts, ", "),
			noun, driftingEnvoys, envoyNoun)

		return nil
	},
}

func init() {
	runsListCmd.Flags().String("envoy", "", "Filter by envoy ID")
	runsListCmd.Flags().Int("limit", 20, "Maximum number of runs to display")
	runsDriftCmd.Flags().String("envoy", "", "Filter by hostname or envoy ID")
	runsCmd.AddCommand(runsListCmd, runsShowCmd, runsDriftCmd)
}
