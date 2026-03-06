package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/isogov/isogov/agent/auth"
	"github.com/spf13/cobra"
)

var dataDir string

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "isogov-agent",
	Short: "Isogov configuration management agent",
	Long:  "Agent daemon that pulls desired state from the Isogov server and applies idempotent resource changes.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Ensure data directory exists
		if err := os.MkdirAll(dataDir, 0700); err != nil {
			return fmt.Errorf("create data directory %s: %w", dataDir, err)
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		logger := slog.Default()

		pub, _, err := auth.LoadOrGenerate(dataDir)
		if err != nil {
			return fmt.Errorf("load or generate keypair: %w", err)
		}

		pemStr, err := auth.PublicKeyPEM(pub)
		if err != nil {
			return fmt.Errorf("export public key PEM: %w", err)
		}

		logger.Info("agent keypair ready", "data_dir", dataDir)
		logger.Info("public key", "pem", pemStr)

		// No convergence loop yet -- that's Phase 2
		logger.Info("agent: no convergence loop configured (Phase 2)")
		return nil
	},
}

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Register this agent with the Isogov server",
	Long:  "Generates a keypair (if needed) and registers with the server using a one-time token.",
	RunE: func(cmd *cobra.Command, args []string) error {
		logger := slog.Default()

		server, _ := cmd.Flags().GetString("server")
		token, _ := cmd.Flags().GetString("token")

		pub, _, err := auth.LoadOrGenerate(dataDir)
		if err != nil {
			return fmt.Errorf("load or generate keypair: %w", err)
		}

		pemStr, err := auth.PublicKeyPEM(pub)
		if err != nil {
			return fmt.Errorf("export public key PEM: %w", err)
		}

		hostname, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("get hostname: %w", err)
		}

		logger.Info("registering with server",
			"server", server,
			"hostname", hostname,
			"data_dir", dataDir,
		)
		logger.Info("public key", "pem", pemStr)

		// Stub: will call Register gRPC RPC with hostname, IP, pubkey PEM, and token
		_ = token
		logger.Info("register: gRPC registration not yet implemented (Plan 01-05)")

		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&dataDir, "data-dir", "/etc/isogov", "directory for agent data (keypair, UUID)")

	registerCmd.Flags().String("server", "localhost:9090", "server gRPC address")
	registerCmd.Flags().String("token", "", "one-time registration token")

	rootCmd.AddCommand(registerCmd)
}
