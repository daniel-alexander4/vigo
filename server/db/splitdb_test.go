// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package db

import "testing"

// TestIsWriteQuery pins the read/write classifier SplitDB routes on. The
// tricky cases are sqlc's comment-prefixed constants and INSERT...RETURNING
// (which sqlc emits as QueryRowContext, so the DBTX method can't classify it).
func TestIsWriteQuery(t *testing.T) {
	cases := []struct {
		name  string
		query string
		write bool
	}{
		{"sqlc insert-returning :one", "-- name: CreateRun :one\nINSERT INTO runs (envoy_id) VALUES (?)\nRETURNING id", true},
		{"sqlc select :one", "-- name: GetRunByID :one\nSELECT id, status FROM runs WHERE id = ?", false},
		{"sqlc select :many", "-- name: ListAllRuns :many\nSELECT id FROM runs ORDER BY started_at DESC", false},
		{"sqlc update :exec", "-- name: FinishRun :exec\nUPDATE runs SET status = ? WHERE id = ?", true},
		{"sqlc delete :exec", "-- name: DeleteRun :exec\nDELETE FROM runs WHERE id = ?", true},
		{"hand-written batch insert", "INSERT INTO run_results (run_id) VALUES (?),(?)", true},
		{"replace", "REPLACE INTO kv (k, v) VALUES (?, ?)", true},
		{"leading whitespace select", "  \n\t SELECT 1", false},
		{"pragma", "PRAGMA foreign_keys = ON", false},
		{"multi-line comment prefix", "-- name: X :many\n-- extra note\nSELECT id FROM runs", false},
		{"comment only", "-- just a comment", false},
		{"lowercase insert", "insert into runs (envoy_id) values (?)", true},
		// Documented limitation: a leading WITH CTE classifies as a read even
		// if it writes. No generated query begins with WITH today; if one is
		// ever added, this case is the canary.
		{"WITH cte select", "WITH x AS (SELECT 1) SELECT * FROM x", false},
	}
	for _, c := range cases {
		if got := isWriteQuery(c.query); got != c.write {
			t.Errorf("%s: isWriteQuery = %v, want %v", c.name, got, c.write)
		}
	}
}
