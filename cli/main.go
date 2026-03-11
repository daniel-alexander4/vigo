package main

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var serverURL string

func main() {
	// When invoked as "hnbvi", behave as "hobnobcli vi <args>".
	if base := filepath.Base(os.Args[0]); base == "hnbvi" {
		args := append([]string{"vi"}, os.Args[1:]...)
		rootCmd.SetArgs(args)
	}

	if err := rootCmd.Execute(); err != nil {
		slog.Error("command failed", "error", err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "hobnobcli",
	Short: "Hobnob admin CLI",
	Long:  "hobnobcli is the administrative CLI for the Hobnob configuration management system.\nIt communicates with the server's REST API to manage enrollment, thralls, and runs.",
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
		webhooksCmd,
		previewCmd,
		rollbackCmd,
		packagesCmd,
		envCmd,
		validateCmd,
		viCmd,
		inventoryCmd,
	)
}
