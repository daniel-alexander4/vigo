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
	Short:         "Hobnob admin CLI",
	Long:          "hobnobcli is the administrative CLI for the Hobnob configuration management system.\nIt communicates with the server's REST API to manage enrollment, thralls, and runs.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&serverURL, "server", "http://localhost:8181",
		"Base URL for the Hobnob REST API")

	rootCmd.AddCommand(
		approvedCmd,
		thrallsCmd,
		tokensCmd,
		pushCmd,
		runsCmd,
		taskCmd,
		queryCmd,
		webhooksCmd,
		previewCmd,
		rollbackCmd,
		packagesCmd,
		envCmd,
		validateCmd,
		viCmd,
		inventoryCmd,
		cliLicenseCmd,
		doctorCmd,
		federationCmd,
		askCmd,
		workflowCmd,
		secretsCmd,
		exportCmd,
		authCmd,
		usersCmd,
		configCmd,
		backupCmd,
		dbCmd,
	)
}
