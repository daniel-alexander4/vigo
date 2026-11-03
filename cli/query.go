// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var queryCmd = &cobra.Command{
	Use:   "livequery <trait-path> --target <spec>",
	Short: "Query a trait value from connected envoys in real time, with cached fallback for offline nodes",
	Long:  "Queries current trait values from connected envoys and cached values from offline envoys.\nExamples:\n  vigocli traits livequery os.distro --target '*.web.*'\n  vigocli traits livequery memory.total_mb --target 'os.family=debian'",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target, _ := cmd.Flags().GetString("target")
		timeout, _ := cmd.Flags().GetInt("timeout")

		if target == "" {
			return fmt.Errorf("--target is required")
		}

		client := newAPIClient()
		var resp struct {
			QueryID   string `json:"queryId"`
			TraitPath string `json:"traitPath"`
			Responded []struct {
				Hostname string `json:"hostname"`
				Value    string `json:"value"`
				Found    bool   `json:"found"`
			} `json:"responded"`
			TimedOut []struct {
				Hostname string `json:"hostname"`
			} `json:"timedOut"`
			Offline []struct {
				Hostname    string `json:"hostname"`
				Value       string `json:"value"`
				Found       bool   `json:"found"`
				CollectedAt string `json:"collectedAt"`
				Stale       bool   `json:"stale"`
			} `json:"offline"`
			Counts struct {
				Responded int `json:"responded"`
				TimedOut  int `json:"timedOut"`
				Offline   int `json:"offline"`
			} `json:"counts"`
		}

		err := client.post("/api/v1/query", map[string]any{
			"traitPath": args[0],
			"target":    target,
			"timeout":   timeout,
		}, &resp)
		if err != nil {
			return err
		}

		fmt.Printf("Query:     %s\n", resp.TraitPath)
		fmt.Printf("Responded: %d  Timed out: %d  Offline: %d\n\n",
			resp.Counts.Responded, resp.Counts.TimedOut, resp.Counts.Offline)

		if len(resp.Responded) > 0 {
			w := newAlignedTable(os.Stdout)
			fmt.Fprintf(w, "%s\t%s\t%s\n", header("HOSTNAME"), header("VALUE"), header("STATUS"))
			for _, r := range resp.Responded {
				val := r.Value
				if !r.Found {
					val = "(not found)"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", r.Hostname, val, "responded")
			}
			w.Flush()
		}

		if len(resp.Offline) > 0 {
			if len(resp.Responded) > 0 {
				fmt.Println()
			}
			w := newAlignedTable(os.Stdout)
			fmt.Fprintf(w, "%s\t%s\t%s\n", header("HOSTNAME"), header("VALUE"), header("STATUS"))
			for _, o := range resp.Offline {
				val := o.Value
				if !o.Found {
					val = "(not found)"
				}
				cached := ""
				if o.CollectedAt != "" {
					cached = fmt.Sprintf("cached %s", formatTimestamp(o.CollectedAt))
				}
				fmt.Fprintf(w, "%s\t%s\toffline %s\n", o.Hostname, val, cached)
			}
			w.Flush()
		}

		if len(resp.TimedOut) > 0 {
			if len(resp.Responded) > 0 || len(resp.Offline) > 0 {
				fmt.Println()
			}
			w := newAlignedTable(os.Stdout)
			fmt.Fprintf(w, "%s\t%s\n", header("HOSTNAME"), header("STATUS"))
			for _, t := range resp.TimedOut {
				fmt.Fprintf(w, "%s\ttimed_out\n", t.Hostname)
			}
			w.Flush()
		}

		if resp.Counts.Responded == 0 && resp.Counts.Offline == 0 && resp.Counts.TimedOut == 0 {
			fmt.Println("No envoys matched the target spec.")
		}

		return nil
	},
}

func init() {
	queryCmd.Flags().String("target", "", "Target spec: hostname, glob ('*.web.*'), UUID, or trait filter (os.distro=ubuntu)")
	queryCmd.Flags().Int("timeout", 10, "Query timeout in seconds")
	traitsCmd.AddCommand(queryCmd)
}
