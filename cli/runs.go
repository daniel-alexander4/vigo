package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var runsCmd = &cobra.Command{
	Use:   "runs",
	Short: "View run history",
}

var runsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent runs",
	RunE: func(cmd *cobra.Command, args []string) error {
		clientID, _ := cmd.Flags().GetString("client")
		limit, _ := cmd.Flags().GetInt("limit")

		path := "/api/v1/runs"
		sep := "?"
		if clientID != "" {
			path += fmt.Sprintf("%sclient_id=%s", sep, clientID)
			sep = "&"
		}
		if limit > 0 {
			path += fmt.Sprintf("%slimit=%d", sep, limit)
		}

		client := newAPIClient()
		var resp struct {
			Runs []struct {
				ID           string `json:"id"`
				ClientID     string `json:"clientId"`
				StartedAt    string `json:"startedAt"`
				Status       string `json:"status"`
				ChangedCount int32  `json:"changedCount"`
				FailedCount  int32  `json:"failedCount"`
			} `json:"runs"`
		}
		if err := client.get(path, &resp); err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tCLIENT\tSTARTED\tSTATUS\tCHANGED\tFAILED")
		for _, r := range resp.Runs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\n",
				r.ID, r.ClientID,
				formatTimestamp(r.StartedAt),
				r.Status, r.ChangedCount, r.FailedCount)
		}
		return w.Flush()
	},
}

var runsShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show details for a run",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Run struct {
				ID           string `json:"id"`
				ClientID     string `json:"clientId"`
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
		fmt.Printf("Client:   %s\n", r.ClientID)
		fmt.Printf("Started:  %s\n", formatTimestamp(r.StartedAt))
		fmt.Printf("Finished: %s\n", formatTimestamp(r.FinishedAt))
		fmt.Printf("Status:   %s\n", r.Status)
		fmt.Printf("Modules:  %d total, %d changed, %d failed, %d skipped\n",
			r.ModuleCount, r.ChangedCount, r.FailedCount, r.SkippedCount)

		if len(resp.Results) > 0 {
			fmt.Println()
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "MODULE\tTYPE\tRESOURCE\tACTION\tCHANGED\tERROR")
			for _, res := range resp.Results {
				errStr := "-"
				if res.ErrorMessage != "" {
					errStr = res.ErrorMessage
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\t%s\n",
					res.ModuleName, res.ResourceType, res.ResourceName,
					res.ActionTaken, res.Changed, errStr)
			}
			w.Flush()
		}

		return nil
	},
}

func init() {
	runsListCmd.Flags().String("client", "", "Filter by client ID")
	runsListCmd.Flags().Int("limit", 20, "Maximum number of runs to display")
	runsCmd.AddCommand(runsListCmd, runsShowCmd)
}
