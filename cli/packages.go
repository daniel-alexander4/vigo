// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var packagesCmd = &cobra.Command{
	Use:   "packages",
	Short: "Compare installed vs desired package versions across the fleet",
}

var packagesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List managed packages with installed and desired versions per thrall",
	RunE: func(cmd *cobra.Command, args []string) error {
		thrallID, _ := cmd.Flags().GetString("thrall")

		path := "/api/v1/packages"
		if thrallID != "" {
			path += "?thrall_id=" + thrallID
		}

		client := newAPIClient()
		var resp struct {
			Packages []struct {
				ThrallID           string `json:"thrallId"`
				Hostname         string `json:"hostname"`
				PackageName      string `json:"packageName"`
				InstalledVersion string `json:"installedVersion"`
				DesiredVersion   string `json:"desiredVersion"`
			} `json:"packages"`
		}
		if err := client.get(path, &resp); err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", header("THRALL"), header("PACKAGE"), header("INSTALLED"), header("DESIRED"))
		for _, p := range resp.Packages {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				p.Hostname, p.PackageName, p.InstalledVersion, p.DesiredVersion)
		}
		return w.Flush()
	},
}

var packagesDriftCmd = &cobra.Command{
	Use:   "drift",
	Short: "Show packages where the installed version doesn't match the desired version",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Drifts []struct {
				ThrallID           string `json:"thrallId"`
				Hostname         string `json:"hostname"`
				PackageName      string `json:"packageName"`
				InstalledVersion string `json:"installedVersion"`
				DesiredVersion   string `json:"desiredVersion"`
				DetectedAt       string `json:"detectedAt"`
			} `json:"drifts"`
		}
		if err := client.get("/api/v1/packages/drift", &resp); err != nil {
			return err
		}

		if len(resp.Drifts) == 0 {
			fmt.Println("No package drift detected")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", header("THRALL"), header("PACKAGE"), header("INSTALLED"), header("DESIRED"), header("DETECTED"))
		for _, d := range resp.Drifts {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				d.Hostname, d.PackageName, d.InstalledVersion,
				d.DesiredVersion, formatTimestamp(d.DetectedAt))
		}
		return w.Flush()
	},
}

func init() {
	packagesListCmd.Flags().String("thrall", "", "Filter by thrall ID")
	packagesCmd.AddCommand(packagesListCmd, packagesDriftCmd)
}
