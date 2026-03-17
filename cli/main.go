// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var serverURL string

// version is set at build time via -ldflags="-X main.version=1.2.3".
var version = "dev"

func main() {
	// When invoked as "hnbvi", behave as "hobnobcli vi <args>".
	if base := filepath.Base(os.Args[0]); base == "hnbvi" {
		args := append([]string{"vi"}, os.Args[1:]...)
		rootCmd.SetArgs(args)
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:           "hobnobcli",
	Short:         "Administrative CLI for the Hobnob distributed state enforcement engine",
	Long:          "hobnobcli communicates with the Hobnob server's REST API to manage enrollment,\nthrall lifecycle, config publishing, orchestration, secrets, and fleet operations.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&serverURL, "server", "https://localhost:8443",
		"Base URL for the Hobnob REST API")

	rootCmd.AddCommand(
		approvedCmd,
		thrallsCmd,
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
		authCmd,
		usersCmd,
		webhooksCmd,
		tokensCmd,
		configCmd,
		backupCmd,
		dbCmd,
		testCmd,
	)
}
