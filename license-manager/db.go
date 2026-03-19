// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

const dbFile = "licenses.db"

func openDB() (*sql.DB, error) {
	dbPath := filepath.Join(keyDir, dbFile)
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return nil, fmt.Errorf("creating directory %s: %w", keyDir, err)
	}

	db, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=on&_journal_mode=wal")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating database: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS customers (
			customer_id   TEXT PRIMARY KEY,
			customer_name TEXT NOT NULL,
			created_at    DATETIME DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS licenses (
			license_id    TEXT PRIMARY KEY,
			customer_id   TEXT NOT NULL REFERENCES customers(customer_id),
			tier          TEXT NOT NULL DEFAULT '',
			max_envoys   INTEGER NOT NULL,
			issued_at     TEXT NOT NULL,
			expires_at    TEXT NOT NULL,
			version       INTEGER NOT NULL DEFAULT 1,
			superseded_by TEXT,
			created_at    DATETIME DEFAULT (datetime('now'))
		);
	`)
	return err
}
