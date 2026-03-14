// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/hobnob/hobnob/server/backup"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// minimalServerConfig is a lightweight struct for parsing just the database
// section from server.yaml (no secrets provider needed).
type minimalServerConfig struct {
	Database struct {
		DSN    string `yaml:"dsn"`
		Driver string `yaml:"driver"`
	} `yaml:"database"`
}

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Backup and restore server state",
	Long: `Create and restore full server backups including database, configs, secrets, and TLS certs.

These commands operate directly on the local filesystem — no server API required.
Run them on the server host.`,
}

var backupCreateCmd = &cobra.Command{
	Use:   "create <path>",
	Short: "Create a full backup archive",
	Long: `Create a tar.gz backup archive containing:
  - SQLite database snapshot (safe with concurrent WAL writes)
  - Config files (hnb/proposed/)
  - Encrypted secrets (secrets/)
  - TLS certificates (tls/)
  - server.yaml

Examples:
  hobnobcli backup create /tmp/hobnob-backup.tar.gz
  hobnobcli backup create /tmp/backup.tar.gz --server-yaml /etc/hobnob-server/server.yaml`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		archivePath := args[0]
		serverYAML, _ := cmd.Flags().GetString("server-yaml")

		// Parse server.yaml to find DB settings.
		data, err := os.ReadFile(serverYAML)
		if err != nil {
			return fmt.Errorf("reading server.yaml at %s: %w", serverYAML, err)
		}

		var cfg minimalServerConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("parsing server.yaml: %w", err)
		}

		dbDriver := cfg.Database.Driver
		if dbDriver == "" {
			dbDriver = "sqlite"
		}
		dbDSN := cfg.Database.DSN
		if dbDSN == "" {
			return fmt.Errorf("database.dsn not set in server.yaml")
		}

		serverRoot, _ := cmd.Flags().GetString("server-root")

		version := os.Getenv("HOBNOB_VERSION")
		if version == "" {
			version = "dev"
		}

		fmt.Printf("Creating backup archive: %s\n", archivePath)
		fmt.Printf("  Database: %s (%s)\n", dbDSN, dbDriver)
		fmt.Printf("  Server root: %s\n", serverRoot)

		err = backup.CreateArchive(archivePath, backup.CreateOptions{
			ServerYAMLPath: serverYAML,
			ServerRoot:     serverRoot,
			DBDriver:       dbDriver,
			DBDSN:          dbDSN,
			HobnobVersion:  version,
		})
		if err != nil {
			return fmt.Errorf("creating backup: %w", err)
		}

		// Print manifest summary.
		m, err := backup.ReadManifest(archivePath)
		if err != nil {
			return fmt.Errorf("reading manifest: %w", err)
		}

		fmt.Printf("\nBackup created successfully.\n")
		fmt.Printf("  Files: %d\n", len(m.Files))
		fmt.Printf("  Version: %s\n", m.HobnobVersion)
		fmt.Printf("  Created: %s\n", m.CreatedAt.Format("2006-01-02T15:04:05Z"))
		return nil
	},
}

var backupRestoreCmd = &cobra.Command{
	Use:   "restore <path>",
	Short: "Restore server state from a backup archive",
	Long: `Restore all server state from a backup archive. Stop the server before restoring.

Components restored:
  - SQLite database (or Postgres via psql)
  - Config files → hnb/proposed/
  - Encrypted secrets → secrets/
  - TLS certificates → tls/
  - server.yaml

Examples:
  hobnobcli backup restore /tmp/hobnob-backup.tar.gz
  hobnobcli backup restore /tmp/backup.tar.gz --dry-run
  hobnobcli backup restore /tmp/backup.tar.gz --db-only
  hobnobcli backup restore /tmp/backup.tar.gz --target /opt/hobnob-server`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		archivePath := args[0]
		targetDir, _ := cmd.Flags().GetString("target")
		dbOnly, _ := cmd.Flags().GetBool("db-only")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		// Read manifest first for display.
		m, err := backup.ReadManifest(archivePath)
		if err != nil {
			return fmt.Errorf("reading manifest: %w", err)
		}

		fmt.Printf("Backup archive: %s\n", archivePath)
		fmt.Printf("  Created: %s\n", m.CreatedAt.Format("2006-01-02T15:04:05Z"))
		fmt.Printf("  Version: %s\n", m.HobnobVersion)
		fmt.Printf("  Database: %s (%s)\n", m.DBDSN, m.DBDriver)
		fmt.Printf("  Files: %d\n", len(m.Files))

		if dryRun {
			fmt.Printf("\nDry run — files that would be restored:\n")
			for _, f := range m.Files {
				if dbOnly && !isDBFile(f) {
					continue
				}
				fmt.Printf("  %s\n", f)
			}
			fmt.Println("\nNo changes made.")
			return nil
		}

		fmt.Printf("\nRestoring to: %s\n", targetDir)
		if dbOnly {
			fmt.Println("  Mode: database only")
		}

		_, err = backup.RestoreArchive(archivePath, backup.RestoreOptions{
			TargetDir: targetDir,
			DBOnly:    dbOnly,
			DryRun:    false,
		})
		if err != nil {
			return fmt.Errorf("restoring backup: %w", err)
		}

		fmt.Println("\nRestore complete. Next steps:")
		fmt.Println("  1. Review server.yaml for any host-specific settings")
		fmt.Println("  2. Run: hobnobcli config publish")
		fmt.Println("  3. Restart hobd (or Docker container)")
		fmt.Println("  4. Agents will reconnect and re-converge automatically")
		return nil
	},
}

var backupInspectCmd = &cobra.Command{
	Use:   "inspect <path>",
	Short: "Show backup archive manifest",
	Long: `Read and display the manifest from a backup archive without extracting.

Examples:
  hobnobcli backup inspect /tmp/hobnob-backup.tar.gz`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		m, err := backup.ReadManifest(args[0])
		if err != nil {
			return err
		}

		data, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	},
}

func isDBFile(path string) bool {
	return path == "db/hobnob.db" || path == "db/dump.sql"
}

func init() {
	backupCreateCmd.Flags().String("server-yaml", "/etc/hobnob-server/server.yaml",
		"Path to server.yaml (used to find DB settings)")
	backupCreateCmd.Flags().String("server-root", "/etc/hobnob-server",
		"Server root directory containing hnb/, secrets/, tls/")

	backupRestoreCmd.Flags().String("target", "/etc/hobnob-server",
		"Root directory to restore into")
	backupRestoreCmd.Flags().Bool("db-only", false,
		"Restore only the database, skip config/secrets/tls")
	backupRestoreCmd.Flags().Bool("dry-run", false,
		"Show what would be restored without writing")

	backupCmd.AddCommand(backupCreateCmd, backupRestoreCmd, backupInspectCmd)
}
