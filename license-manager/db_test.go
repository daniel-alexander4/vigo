// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"testing"
)

func TestMigrateCreatesSchema(t *testing.T) {
	origKeyDir := keyDir
	keyDir = t.TempDir()
	defer func() { keyDir = origKeyDir }()

	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	// Verify customers table exists.
	var name string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='customers'").Scan(&name)
	if err != nil {
		t.Fatalf("customers table not found: %v", err)
	}

	// Verify licenses table exists.
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='licenses'").Scan(&name)
	if err != nil {
		t.Fatalf("licenses table not found: %v", err)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	origKeyDir := keyDir
	keyDir = t.TempDir()
	defer func() { keyDir = origKeyDir }()

	db, err := openDB()
	if err != nil {
		t.Fatalf("first openDB: %v", err)
	}

	// Run migrate again — should not error.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	db.Close()
}

func TestCustomerCRUD(t *testing.T) {
	origKeyDir := keyDir
	keyDir = t.TempDir()
	defer func() { keyDir = origKeyDir }()

	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	// Insert a customer.
	_, err = db.Exec("INSERT INTO customers (customer_id, customer_name) VALUES (?, ?)", "acme", "Acme Corp")
	if err != nil {
		t.Fatalf("insert customer: %v", err)
	}

	// Query back.
	var name string
	err = db.QueryRow("SELECT customer_name FROM customers WHERE customer_id = ?", "acme").Scan(&name)
	if err != nil {
		t.Fatalf("query customer: %v", err)
	}
	if name != "Acme Corp" {
		t.Errorf("customer_name = %q, want Acme Corp", name)
	}
}
