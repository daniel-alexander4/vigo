// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export compliance data (SIEM, CMDB, OSCAL)",
}

var exportSIEMCmd = &cobra.Command{
	Use:   "siem",
	Short: "Export SIEM events from run results",
	Long: `Export run results as SIEM-consumable JSON events.

Examples:
  hobnobcli export siem
  hobnobcli export siem --since 2026-03-12T00:00:00Z
  hobnobcli export siem --thrall web-01.prod.example.com --limit 500
  hobnobcli export siem --output events.json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		since, _ := cmd.Flags().GetString("since")
		thrall, _ := cmd.Flags().GetString("thrall")
		limit, _ := cmd.Flags().GetInt("limit")
		output, _ := cmd.Flags().GetString("output")

		params := url.Values{}
		if since != "" {
			params.Set("since", since)
		}
		if thrall != "" {
			params.Set("thrall", thrall)
		}
		if limit > 0 {
			params.Set("limit", fmt.Sprintf("%d", limit))
		}

		path := "/api/v1/export/siem"
		if encoded := params.Encode(); encoded != "" {
			path += "?" + encoded
		}

		client := newAPIClient()
		var raw json.RawMessage
		if err := client.get(path, &raw); err != nil {
			return err
		}

		return writeExportOutput(raw, output)
	},
}

var exportCMDBCmd = &cobra.Command{
	Use:   "cmdb",
	Short: "Export CMDB configuration items from fleet data",
	Long: `Export thrall data as CMDB-consumable JSON records.

Examples:
  hobnobcli export cmdb
  hobnobcli export cmdb --status drifted
  hobnobcli export cmdb --environment prod
  hobnobcli export cmdb --output cmdb.json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		status, _ := cmd.Flags().GetString("status")
		environment, _ := cmd.Flags().GetString("environment")
		output, _ := cmd.Flags().GetString("output")

		params := url.Values{}
		if status != "" {
			params.Set("status", status)
		}
		if environment != "" {
			params.Set("environment", environment)
		}

		path := "/api/v1/export/cmdb"
		if encoded := params.Encode(); encoded != "" {
			path += "?" + encoded
		}

		client := newAPIClient()
		var raw json.RawMessage
		if err := client.get(path, &raw); err != nil {
			return err
		}

		return writeExportOutput(raw, output)
	},
}

var exportOSCALCmd = &cobra.Command{
	Use:   "oscal",
	Short: "Export OSCAL assessment results stub",
	Long: `Export a minimal OSCAL Assessment Results JSON envelope.

Examples:
  hobnobcli export oscal
  hobnobcli export oscal --output oscal.json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		output, _ := cmd.Flags().GetString("output")

		client := newAPIClient()
		var raw json.RawMessage
		if err := client.get("/api/v1/export/oscal", &raw); err != nil {
			return err
		}

		return writeExportOutput(raw, output)
	},
}

func writeExportOutput(raw json.RawMessage, outputPath string) error {
	formatted, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to format response: %w", err)
	}

	if outputPath != "" {
		if err := os.WriteFile(outputPath, append(formatted, '\n'), 0644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		fmt.Printf("Written to %s\n", outputPath)
		return nil
	}

	fmt.Fprintln(os.Stdout, string(formatted))
	return nil
}

func init() {
	exportSIEMCmd.Flags().String("since", "", "Only include events after this time (RFC3339)")
	exportSIEMCmd.Flags().String("thrall", "", "Filter by thrall hostname")
	exportSIEMCmd.Flags().Int("limit", 0, "Maximum number of events (default: 1000, max: 10000)")
	exportSIEMCmd.Flags().String("output", "", "Write output to file instead of stdout")

	exportCMDBCmd.Flags().String("status", "", "Filter by compliance status (compliant|drifted|failing|stale)")
	exportCMDBCmd.Flags().String("environment", "", "Filter by environment (e.g. prod)")
	exportCMDBCmd.Flags().String("output", "", "Write output to file instead of stdout")

	exportOSCALCmd.Flags().String("output", "", "Write output to file instead of stdout")

	exportCmd.AddCommand(exportSIEMCmd, exportCMDBCmd, exportOSCALCmd)
}
