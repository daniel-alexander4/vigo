package main

import (
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

var serverURL string

func main() {
	if err := rootCmd.Execute(); err != nil {
		slog.Error("command failed", "error", err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "hobcli",
	Short: "Hobnob admin CLI",
	Long:  "hobcli is the administrative CLI for the Hobnob configuration management system.\nIt communicates with the server's REST API to manage enrollment, nodes, and runs.",
}

func init() {
	rootCmd.PersistentFlags().StringVar(&serverURL, "server", "http://localhost:8085",
		"Base URL for the Hobnob REST API")

	rootCmd.AddCommand(
		approvedCmd,
		nodesCmd,
		tokensCmd,
		pushCmd,
		runsCmd,
		webhooksCmd,
		previewCmd,
		rollbackCmd,
		packagesCmd,
		envCmd,
		validateCmd,
		viCmd,
	)
}
