// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var serverURL string

// version is set at build time via -ldflags="-X main.version=1.2.3".
// The default should match the VERSION file at the repo root.
var version = "0.1.3"

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:           "vigogo",
	Short:         "Administrative CLI for the Vigo distributed state enforcement engine",
	Long:          "vigogo communicates with the Vigo server's REST API to manage enrollment,\nenvoy lifecycle, config publishing, orchestration, secrets, and fleet operations.",
	Version:       version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&serverURL, "server", "https://localhost:8443",
		"Base URL for the Vigo REST API")

	rootCmd.AddCommand(
		approvedCmd,
		envoysCmd,
		runsCmd,
		taskCmd,
		queryCmd,
		viCmd,
		inventoryCmd,
		cliLicenseCmd,
		doctorCmd,
		spannerCmd,
		askCmd,
		workflowCmd,
		secretsCmd,
		exportCmd,
		reportCmd,
		remediationCmd,
		authCmd,
		webusersCmd,
		webhooksCmd,
		tokensCmd,
		configCmd,
		backupCmd,
		dbCmd,
		testCmd,
		serverCmd,
		riskCmd,
	)
}
