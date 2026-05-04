// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "Inspect and maintain the server's SQLite database",
	Long:  "Inspect, maintain, and query the server database.",
}

var dbStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show database file size, WAL size, page count, and per-table row counts",
	Long:  "Display database file size, WAL size, page count, and table row counts.",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var stats map[string]any
		if err := client.get("/api/v1/db/stats", &stats); err != nil {
			return err
		}
		fmt.Printf("Driver:     %s\n", stats["driver"])
		if v, ok := stats["file_size"]; ok {
			fmt.Printf("File size:  %s\n", formatBytes(toInt64(v)))
		}
		if v, ok := stats["wal_size"]; ok {
			fmt.Printf("WAL size:   %s\n", formatBytes(toInt64(v)))
		}
		if v, ok := stats["page_count"]; ok {
			fmt.Printf("Pages:      %v (size: %v bytes)\n", v, stats["page_size"])
		}
		if tables, ok := stats["tables"].(map[string]any); ok {
			fmt.Println("\nTables:")
			for name, count := range tables {
				fmt.Printf("  %-30s %v rows\n", name, count)
			}
		}
		return nil
	},
}

var dbTablesCmd = &cobra.Command{
	Use:   "tables",
	Short: "List all database tables with their current row counts",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var tables map[string]any
		if err := client.get("/api/v1/db/tables", &tables); err != nil {
			return err
		}
		fmt.Printf("%-30s %s\n", "TABLE", "ROWS")
		fmt.Printf("%-30s %s\n", "-----", "----")
		for name, count := range tables {
			fmt.Printf("%-30s %v\n", name, count)
		}
		return nil
	},
}

var dbIntegrityCmd = &cobra.Command{
	Use:   "integrity",
	Short: "Run PRAGMA integrity_check to verify database consistency",
	Long:  "Runs PRAGMA integrity_check (SQLite) or connection health check (Postgres).",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var result map[string]any
		if err := client.get("/api/v1/db/integrity", &result); err != nil {
			return err
		}
		fmt.Printf("Driver:  %s\n", result["driver"])
		fmt.Printf("Status:  %s\n", result["status"])
		if v, ok := result["foreign_key_violations"]; ok {
			fmt.Printf("FK violations: %v\n", v)
			if details, ok := result["foreign_key_details"].([]any); ok {
				for _, d := range details {
					fmt.Printf("  %v\n", d)
				}
			}
		}
		if result["ok"] == true {
			fmt.Println("\nDatabase is healthy.")
		} else {
			fmt.Println("\nDatabase has issues.")
			return fmt.Errorf("integrity check failed")
		}
		return nil
	},
}

var dbVacuumCmd = &cobra.Command{
	Use:   "vacuum",
	Short: "Run VACUUM to reclaim unused disk space from the database file",
	Long: `Runs VACUUM on the SQLite database to reclaim space from deleted rows.
This operation rewrites the entire database file and may take a few seconds
on large databases. The server remains available during VACUUM.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			if !confirmAction("This will rewrite the entire database file. Continue?") {
				fmt.Println("Cancelled.")
				return nil
			}
		}

		client := newAPIClient()
		var result map[string]any
		if err := client.post("/api/v1/db/vacuum", nil, &result); err != nil {
			return err
		}
		fmt.Printf("Before:    %s\n", formatBytes(toInt64(result["before"])))
		fmt.Printf("After:     %s\n", formatBytes(toInt64(result["after"])))
		fmt.Printf("Reclaimed: %s\n", formatBytes(toInt64(result["reclaimed"])))
		return nil
	},
}

var dbPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Delete runs/tasks/workflows/history older than --older-than (one-shot)",
	Long: `Run a one-shot retention prune. Deletes from runs, push_runs,
workflow_runs, file_snapshots, convergence_history, and compliance_history
where the row is older than the given age. Same query path as the daily
background pruner.

Use this once after enabling 'database.retention' in server.yaml on a DB
that has accumulated more history than the new retention window allows —
the daily pruner only catches up gradually. Pair with 'vigocli db vacuum'
to reclaim disk space after the delete.

Examples:
  vigocli db prune --older-than 30d
  vigocli db prune --older-than 720h`,
	RunE: func(cmd *cobra.Command, args []string) error {
		olderThan, _ := cmd.Flags().GetString("older-than")
		if olderThan == "" {
			return fmt.Errorf("--older-than is required (e.g. 30d, 720h)")
		}
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			if !confirmAction(fmt.Sprintf("Delete history rows older than %s. Continue?", olderThan)) {
				fmt.Println("Cancelled.")
				return nil
			}
		}

		client := newAPIClient()
		var result map[string]any
		if err := client.post("/api/v1/db/prune", map[string]string{"older_than": olderThan}, &result); err != nil {
			return err
		}
		fmt.Printf("Older than: %v\n", result["older_than"])
		fmt.Printf("Before:     %s\n", formatBytes(toInt64(result["before"])))
		fmt.Printf("After:      %s\n", formatBytes(toInt64(result["after"])))
		fmt.Printf("Reclaimed:  %s\n", formatBytes(toInt64(result["reclaimed"])))
		fmt.Println("\nNote: VACUUM is needed to return reclaimed pages to the OS.")
		fmt.Println("Run: vigocli db vacuum")
		return nil
	},
}

var dbCheckpointCmd = &cobra.Command{
	Use:   "checkpoint",
	Short: "Force a WAL checkpoint to flush pending writes to the main database file",
	Long: `Forces a WAL checkpoint, writing all pending WAL pages to the main database
file and truncating the WAL. Useful before taking a backup to ensure the
database file is self-contained.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var result map[string]any
		if err := client.post("/api/v1/db/checkpoint", nil, &result); err != nil {
			return err
		}
		fmt.Printf("Pages written: %v\n", result["pages_written"])
		fmt.Printf("Pages total:   %v\n", result["pages_total"])
		fmt.Println("Checkpoint complete.")
		return nil
	},
}

var dbQueryCmd = &cobra.Command{
	Use:   "query <SQL>",
	Short: "Execute a read-only SQL query and display results as a table or JSON",
	Long: `Execute a read-only SELECT query against the database. Write operations
(INSERT, UPDATE, DELETE, DROP, etc.) are blocked.

Examples:
  vigocli db query "SELECT id, hostname FROM envoys LIMIT 10"
  vigocli db query "SELECT COUNT(*) FROM runs WHERE status = 'failure'"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var result struct {
			Columns []string         `json:"columns"`
			Rows    []map[string]any `json:"rows"`
			Count   int              `json:"count"`
		}
		if err := client.post("/api/v1/db/query", map[string]string{"sql": args[0]}, &result); err != nil {
			return err
		}

		if len(result.Columns) == 0 {
			fmt.Println("(no results)")
			return nil
		}

		// Print header.
		for i, col := range result.Columns {
			if i > 0 {
				fmt.Print("\t")
			}
			fmt.Print(col)
		}
		fmt.Println()

		// Print rows.
		for _, row := range result.Rows {
			for i, col := range result.Columns {
				if i > 0 {
					fmt.Print("\t")
				}
				fmt.Print(formatValue(row[col]))
			}
			fmt.Println()
		}

		fmt.Printf("\n(%d rows)\n", result.Count)
		return nil
	},
}

var dbResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Drop all tables and recreate the schema from scratch (DESTROYS ALL DATA)",
	Long: `Deletes the SQLite database file and WAL. The server will recreate the
database from migrations on next restart.

WARNING: This permanently destroys all data — envoys, runs, traits,
tokens, users, audit history. This action cannot be undone.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			fmt.Println("WARNING: This will permanently delete ALL data in the database.")
			fmt.Println("All envoys, runs, traits, tokens, users, and audit history will be lost.")
			fmt.Println("The server must be restarted after reset.")
			fmt.Print("\nType 'yes' to confirm: ")
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			if strings.TrimSpace(answer) != "yes" {
				fmt.Println("Cancelled.")
				return nil
			}
		}


		// Get database path from server stats.
		client := newAPIClient()
		var stats map[string]any
		if err := client.get("/api/v1/db/stats", &stats); err != nil {
			return fmt.Errorf("cannot reach server: %w", err)
		}

		if stats["driver"] != "sqlite" {
			return fmt.Errorf("db reset is only available for SQLite (current driver: %v)", stats["driver"])
		}

		fmt.Println("Database reset requested. Stop the server and delete the database file manually:")
		fmt.Println()
		fmt.Println("  1. Stop the server: docker compose down")
		fmt.Println("  2. Delete the database: rm -f /srv/vigo/db/vigo.db*")
		fmt.Println("  3. Restart: docker compose up -d")
		fmt.Println()
		fmt.Println("The server recreates the database from migrations on startup.")

		return nil
	},
}

func init() {
	dbVacuumCmd.Flags().Bool("force", false, "skip confirmation prompt")
	dbResetCmd.Flags().Bool("force", false, "skip confirmation prompt")
	dbPruneCmd.Flags().String("older-than", "", "age cutoff (e.g. 30d, 720h); rows older than this are deleted")
	dbPruneCmd.Flags().Bool("force", false, "skip confirmation prompt")

	dbCmd.AddCommand(
		dbStatsCmd,
		dbTablesCmd,
		dbIntegrityCmd,
		dbVacuumCmd,
		dbPruneCmd,
		dbCheckpointCmd,
		dbQueryCmd,
		dbResetCmd,
	)
}

// formatBytes formats bytes into human-readable form.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// toInt64 converts a JSON number to int64.
func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	default:
		return 0
	}
}

// formatValue formats a value for tabular display.
func formatValue(v any) string {
	if v == nil {
		return "NULL"
	}
	return fmt.Sprintf("%v", v)
}
