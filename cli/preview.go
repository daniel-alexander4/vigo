// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"git.alexander4.io/vigo/server/convergence"
)

func colorAction(action string) string {
	switch convergence.ClassifyAction(action) {
	case convergence.ActionCreate:
		return green(action)
	case convergence.ActionUpdate:
		return yellow(action)
	case convergence.ActionDelete:
		return red(action)
	}
	switch action {
	case "ok", "none", "noop", "settled":
		return dim(action)
	default:
		return action
	}
}

var previewCmd = &cobra.Command{
	Use:   "preview [hostname-or-uuid]",
	Short: "Dry-run config resolution for a envoy to see what resources would change without applying",
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

func previewNode(idOrHostname string) error {
	client := newAPIClient()
	envoyID, err := resolveEnvoy(client, idOrHostname)
	if err != nil {
		return err
	}
	var resp struct {
		Actions []struct {
			ConfigcrateName    string `json:"moduleName"`
			ResourceType  string `json:"resourceType"`
			ResourceName  string `json:"resourceName"`
			PlannedAction string `json:"plannedAction"`
		} `json:"actions"`
	}
	if err := client.post("/api/v1/envoys/"+envoyID+"/dry-run", map[string]any{}, &resp); err != nil {
		return err
	}

	if len(resp.Actions) == 0 {
		fmt.Printf("No changes planned for %s\n", envoyID)
		return nil
	}

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "MODULE\tTYPE\tRESOURCE\tACTION\n")
	for _, a := range resp.Actions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			a.ConfigcrateName, a.ResourceType, a.ResourceName, colorAction(a.PlannedAction))
	}
	return w.Flush()
}

func previewMultiple(envFilter string) error {
	client := newAPIClient()
	envoys, err := listAllEnvoys(client, false)
	if err != nil {
		return err
	}

	for _, c := range envoys {
		fmt.Printf("--- %s (%s) ---\n", c.Hostname, c.ID)
		if err := previewNode(c.ID); err != nil {
			fmt.Fprintf(os.Stderr, "Error previewing %s: %v\n", c.Hostname, err)
		}
		fmt.Println()
	}
	return nil
}

func init() {
	previewCmd.Flags().Bool("all", false, "Preview all envoys")
	previewCmd.Flags().String("env", "", "Filter envoys by environment")
}
