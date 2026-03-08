package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// ANSI color codes for action types
const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
)

func colorAction(action string) string {
	// Only colorize if stdout is a terminal
	if fileInfo, _ := os.Stdout.Stat(); fileInfo != nil && fileInfo.Mode()&os.ModeCharDevice == 0 {
		return action
	}
	switch action {
	case "create":
		return colorGreen + action + colorReset
	case "update":
		return colorYellow + action + colorReset
	case "delete":
		return colorRed + action + colorReset
	default:
		return action
	}
}

var previewCmd = &cobra.Command{
	Use:   "preview [hostname-or-uuid]",
	Short: "Dry-run to preview planned changes",
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

func previewNode(nodeID string) error {
	client := newAPIClient()
	var resp struct {
		Actions []struct {
			ModuleName    string `json:"moduleName"`
			ResourceType  string `json:"resourceType"`
			ResourceName  string `json:"resourceName"`
			PlannedAction string `json:"plannedAction"`
		} `json:"actions"`
	}
	if err := client.post("/api/v1/nodes/"+nodeID+"/dry-run", map[string]any{}, &resp); err != nil {
		return err
	}

	if len(resp.Actions) == 0 {
		fmt.Printf("No changes planned for %s\n", nodeID)
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
		Nodes []struct {
			ID       string `json:"id"`
			Hostname string `json:"hostname"`
		} `json:"nodes"`
	}
	if err := client.get("/api/v1/nodes", &listResp); err != nil {
		return err
	}

	for _, c := range listResp.Nodes {
		fmt.Printf("--- %s (%s) ---\n", c.Hostname, c.ID)
		if err := previewNode(c.ID); err != nil {
			fmt.Fprintf(os.Stderr, "Error previewing %s: %v\n", c.Hostname, err)
		}
		fmt.Println()
	}
	return nil
}

func init() {
	previewCmd.Flags().Bool("all", false, "Preview all nodes")
	previewCmd.Flags().String("env", "", "Filter nodes by environment")
}
