// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func colorAction(action string) string {
	switch action {
	case "create":
		return green(action)
	case "update":
		return yellow(action)
	case "delete":
		return red(action)
	default:
		return action
	}
}

var previewCmd = &cobra.Command{
	Use:   "preview [hostname-or-uuid]",
	Short: "Dry-run config resolution for a thrall to see what resources would change without applying",
	RunE: func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")
		env, _ := cmd.Flags().GetString("env")

		if all || env != "" {
			return previewMultiple(env)
		}

		if len(args) < 1 {
			return fmt.Errorf("specify a hostname or UUID, or use --all / --env")
		}

		return previewNode(args[0])
	},
}

func previewNode(thrallID string) error {
	client := newAPIClient()
	var resp struct {
		Actions []struct {
			ModuleName    string `json:"moduleName"`
			ResourceType  string `json:"resourceType"`
			ResourceName  string `json:"resourceName"`
			PlannedAction string `json:"plannedAction"`
		} `json:"actions"`
	}
	if err := client.post("/api/v1/thralls/"+thrallID+"/dry-run", map[string]any{}, &resp); err != nil {
		return err
	}

	if len(resp.Actions) == 0 {
		fmt.Printf("No changes planned for %s\n", thrallID)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "MODULE\tTYPE\tRESOURCE\tACTION\n")
	for _, a := range resp.Actions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			a.ModuleName, a.ResourceType, a.ResourceName, colorAction(a.PlannedAction))
	}
	return w.Flush()
}

func previewMultiple(envFilter string) error {
	client := newAPIClient()
	var listResp struct {
		Thralls []struct {
			ID       string `json:"id"`
			Hostname string `json:"hostname"`
		} `json:"thralls"`
	}
	if err := client.get("/api/v1/thralls", &listResp); err != nil {
		return err
	}

	for _, c := range listResp.Thralls {
		fmt.Printf("--- %s (%s) ---\n", c.Hostname, c.ID)
		if err := previewNode(c.ID); err != nil {
			fmt.Fprintf(os.Stderr, "Error previewing %s: %v\n", c.Hostname, err)
		}
		fmt.Println()
	}
	return nil
}

func init() {
	previewCmd.Flags().Bool("all", false, "Preview all thralls")
	previewCmd.Flags().String("env", "", "Filter thralls by environment")
}
