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
	Use:   "gvctl",
	Short: "Isogov admin CLI",
	Long:  "gvctl is the administrative CLI for the Isogov configuration management system.\nIt communicates with the server's REST API to manage enrollment, clients, and runs.",
}

func init() {
	rootCmd.PersistentFlags().StringVar(&serverURL, "server", "http://localhost:8080",
		"Base URL for the Isogov REST API")

	rootCmd.AddCommand(
		approvedCmd,
		clientsCmd,
		tokensCmd,
		pushCmd,
		runsCmd,
		webhooksCmd,
		previewCmd,
		rollbackCmd,
		packagesCmd,
		envCmd,
	)
}
