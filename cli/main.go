package main

import (
	"fmt"
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

	// Subcommand: approved
	approvedCmd := &cobra.Command{
		Use:   "approved",
		Short: "Manage approved enrollment patterns",
	}
	approvedCmd.AddCommand(
		&cobra.Command{
			Use:   "add",
			Short: "Add an approved pattern with a one-time token",
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Println("gvctl approved add: not yet implemented (Phase 3)")
				return nil
			},
		},
		&cobra.Command{
			Use:   "list",
			Short: "List all approved patterns",
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Println("gvctl approved list: not yet implemented (Phase 3)")
				return nil
			},
		},
		&cobra.Command{
			Use:   "revoke",
			Short: "Revoke an approved pattern",
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Println("gvctl approved revoke: not yet implemented (Phase 3)")
				return nil
			},
		},
	)

	// Subcommand: clients
	clientsCmd := &cobra.Command{
		Use:   "clients",
		Short: "Manage enrolled clients",
	}
	clientsCmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List all enrolled clients",
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Println("gvctl clients list: not yet implemented (Phase 3)")
				return nil
			},
		},
		&cobra.Command{
			Use:   "show [id]",
			Short: "Show details for a client",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Printf("gvctl clients show %s: not yet implemented (Phase 3)\n", args[0])
				return nil
			},
		},
		&cobra.Command{
			Use:   "revoke [id]",
			Short: "Revoke an enrolled client",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Printf("gvctl clients revoke %s: not yet implemented (Phase 3)\n", args[0])
				return nil
			},
		},
	)

	// Subcommand: tokens
	tokensCmd := &cobra.Command{
		Use:   "tokens",
		Short: "Manage enrollment tokens",
	}
	tokensCmd.AddCommand(
		&cobra.Command{
			Use:   "generate",
			Short: "Generate a new one-time enrollment token",
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Println("gvctl tokens generate: not yet implemented (Phase 3)")
				return nil
			},
		},
	)

	// Subcommand: push
	pushCmd := &cobra.Command{
		Use:   "push",
		Short: "Trigger an immediate check-in for a client or all clients",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("gvctl push: not yet implemented (Phase 3)")
			return nil
		},
	}

	// Subcommand: runs
	runsCmd := &cobra.Command{
		Use:   "runs",
		Short: "View run history",
	}
	runsCmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List recent runs",
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Println("gvctl runs list: not yet implemented (Phase 3)")
				return nil
			},
		},
		&cobra.Command{
			Use:   "show [id]",
			Short: "Show details for a run",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Printf("gvctl runs show %s: not yet implemented (Phase 3)\n", args[0])
				return nil
			},
		},
	)

	rootCmd.AddCommand(approvedCmd, clientsCmd, tokensCmd, pushCmd, runsCmd)
}
