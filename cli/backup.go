// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daniel-alexander4/vigo/server/backup"
	"github.com/daniel-alexander4/vigo/server/secrets"
	"github.com/spf13/cobra"
	"golang.org/x/term"
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
	Short: "Create and restore full server backups (database, configs, secrets, TLS certs)",
	Long: `Create and restore full server backups including database, configs, secrets, and TLS certs.

These commands operate directly on the local filesystem — no server API required.
Run them on the server host.`,
}

var backupCreateCmd = &cobra.Command{
	Use:   "create <path>",
	Short: "Snapshot the database, configs, secrets, and TLS certs into a tar.gz archive",
	Long: `Create a tar.gz backup archive containing:
  - SQLite database snapshot (safe with concurrent WAL writes)
  - Config files (policy/stage/)
  - Encrypted secrets (secrets/)
  - TLS certificates (tls/)
  - server.yaml

Examples:
  vigogo backup create /tmp/vigo-backup.tar.gz
  vigogo backup create /tmp/backup.tar.gz --server-yaml /srv/vigo/server.yaml`,
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

		// Resolve secret: references via the local secrets provider.
		if strings.HasPrefix(dbDSN, "secret:") {
			secretPath := strings.TrimPrefix(dbDSN, "secret:")
			secretsDir := filepath.Join(serverRoot, "secrets")
			provider, provErr := secrets.NewLocalProvider("", secretsDir)
			if provErr != nil {
				return fmt.Errorf("initializing secrets provider: %w", provErr)
			}
			defer provider.Close()
			resolved, getErr := provider.Get(context.Background(), secretPath)
			if getErr != nil {
				return fmt.Errorf("resolving %s: %w", dbDSN, getErr)
			}
			dbDSN = resolved
		}

		// version is set at package level via -ldflags at build time.

		fmt.Printf("Creating backup archive: %s\n", archivePath)
		fmt.Printf("  Database: %s (%s)\n", dbDSN, dbDriver)
		fmt.Printf("  Server root: %s\n", serverRoot)

		err = backup.CreateArchive(archivePath, backup.CreateOptions{
			ServerYAMLPath: serverYAML,
			ServerRoot:     serverRoot,
			DBDriver:       dbDriver,
			DBDSN:          dbDSN,
			VigoVersion:  version,
		})
		if err != nil {
			return fmt.Errorf("creating backup: %w", err)
		}

		// Compute SHA-256 and write sidecar file.
		hash, err := backup.HashFile(archivePath)
		if err != nil {
			return fmt.Errorf("computing hash: %w", err)
		}
		if err := backup.WriteHashFile(archivePath, hash); err != nil {
			return fmt.Errorf("writing hash file: %w", err)
		}

		// Encrypt if requested.
		encrypt, _ := cmd.Flags().GetBool("encrypt")
		if encrypt {
			pw, err := promptBackupPassword("Enter encryption password: ")
			if err != nil {
				return err
			}
			fmt.Println("Encrypting archive...")
			if err := backup.EncryptFile(archivePath, pw); err != nil {
				return fmt.Errorf("encrypting archive: %w", err)
			}
			// Recompute hash of encrypted file and update sidecar.
			hash, err = backup.HashFile(archivePath)
			if err != nil {
				return fmt.Errorf("computing encrypted hash: %w", err)
			}
			if err := backup.WriteHashFile(archivePath, hash); err != nil {
				return fmt.Errorf("writing hash file: %w", err)
			}
		}

		// Print manifest summary.
		if !encrypt {
			m, err := backup.ReadManifest(archivePath)
			if err != nil {
				return fmt.Errorf("reading manifest: %w", err)
			}
			fmt.Printf("\nBackup created successfully.\n")
			fmt.Printf("  Files: %d\n", len(m.Files))
			fmt.Printf("  Version: %s\n", m.VigoVersion)
			fmt.Printf("  Created: %s\n", m.CreatedAt.Format("2006-01-02T15:04:05Z"))
		} else {
			fmt.Printf("\nEncrypted backup created successfully.\n")
		}
		fmt.Printf("  SHA-256: %s\n", hash)
		fmt.Printf("  Hash file: %s.sha256\n", archivePath)
		return nil
	},
}

var backupRestoreCmd = &cobra.Command{
	Use:   "restore <path>",
	Short: "Restore the database, configs, secrets, and TLS certs from a backup archive",
	Long: `Restore all server state from a backup archive. Stop the server before restoring.

Components restored:
  - SQLite database (or Postgres via psql)
  - Config files → policy/stage/
  - Encrypted secrets → secrets/
  - TLS certificates → tls/
  - server.yaml

Examples:
  vigogo backup restore /tmp/vigo-backup.tar.gz
  vigogo backup restore /tmp/backup.tar.gz --dry-run
  vigogo backup restore /tmp/backup.tar.gz --db-only
  vigogo backup restore /tmp/backup.tar.gz --target /srv/vigo`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		archivePath := args[0]
		targetDir, _ := cmd.Flags().GetString("target")
		dbOnly, _ := cmd.Flags().GetBool("db-only")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		decrypt, _ := cmd.Flags().GetBool("decrypt")

		// Verify SHA-256 if sidecar file exists.
		if hash, err := backup.VerifyHashFile(archivePath); err == nil {
			fmt.Printf("SHA-256 verified: %s\n", hash)
		} else if !os.IsNotExist(err) {
			fmt.Printf("WARNING: %s\n", err)
		}

		// Decrypt if requested.
		if decrypt {
			pw, err := promptBackupPassword("Enter decryption password: ")
			if err != nil {
				return err
			}
			fmt.Println("Decrypting archive...")
			if err := backup.DecryptFile(archivePath, pw); err != nil {
				return fmt.Errorf("decrypting archive: %w", err)
			}
			fmt.Println("Archive decrypted.")
		}

		// Read manifest first for display.
		m, err := backup.ReadManifest(archivePath)
		if err != nil {
			return fmt.Errorf("reading manifest: %w", err)
		}

		fmt.Printf("Backup archive: %s\n", archivePath)
		fmt.Printf("  Created: %s\n", m.CreatedAt.Format("2006-01-02T15:04:05Z"))
		fmt.Printf("  Version: %s\n", m.VigoVersion)
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

		if !confirmAction("Restore from this backup? Current data will be overwritten.") {
			fmt.Println("Cancelled.")
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
		fmt.Println("  2. Run: vigogo config publish")
		fmt.Println("  3. Restart vigosrv (or Docker container)")
		fmt.Println("  4. Agents will reconnect and re-converge automatically")
		return nil
	},
}

var backupInspectCmd = &cobra.Command{
	Use:   "inspect <path>",
	Short: "List the files and metadata inside a backup archive without extracting",
	Long: `Read and display the manifest from a backup archive without extracting.

Examples:
  vigogo backup inspect /tmp/vigo-backup.tar.gz`,
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
	return path == "db/vigo.db" || path == "db/dump.sql"
}

// promptBackupPassword reads a password for backup encryption/decryption.
func promptBackupPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}
	if len(pw) == 0 {
		return "", fmt.Errorf("password cannot be empty")
	}
	return string(pw), nil
}

func init() {
	backupCreateCmd.Flags().String("server-yaml", "/srv/vigo/server.yaml",
		"Path to server.yaml (used to find DB settings)")
	backupCreateCmd.Flags().String("server-root", "/srv/vigo",
		"Server root directory containing policy/, secrets/, tls/")
	backupCreateCmd.Flags().Bool("encrypt", false,
		"Encrypt the archive with AES-256-GCM (prompts for password)")

	backupRestoreCmd.Flags().String("target", "/srv/vigo",
		"Root directory to restore into")
	backupRestoreCmd.Flags().Bool("db-only", false,
		"Restore only the database, skip config/secrets/tls")
	backupRestoreCmd.Flags().Bool("dry-run", false,
		"Show what would be restored without writing")
	backupRestoreCmd.Flags().Bool("decrypt", false,
		"Decrypt the archive before restoring (prompts for password)")

	backupCmd.AddCommand(backupCreateCmd, backupRestoreCmd, backupInspectCmd)
}
