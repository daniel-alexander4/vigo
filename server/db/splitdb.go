// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import (
	"context"
	"database/sql"
	"strings"
)

// SplitDB is a DBTX that routes writes to a single-writer pool and reads to a
// concurrent read pool. It exists for the SQLite backend: SQLite serializes
// all writes behind one lock, and the CGO driver (mattn/go-sqlite3) pins an OS
// thread for the duration of every blocked call. An unbounded pool therefore
// turns a write storm into thread exhaustion. Pinning writes to a
// MaxOpenConns(1) pool makes excess writers park cheaply on the connection
// queue (no thread, no lock collision) while reads stay concurrent on the
// read pool — WAL allows concurrent readers alongside the single writer.
//
// Mirrors the PgDBTX wrapper pattern (pgcompat.go): a thin DBTX adapter that
// the Queries layer is constructed over, so routing is transparent to every
// call site. Postgres does not use this — it has real concurrent writers and a
// pure-Go driver, so it keeps PgDBTX on a single pool.
type SplitDB struct {
	read  *sql.DB
	write *sql.DB
}

// NewSplitDB returns a DBTX routing reads to read and writes to write.
func NewSplitDB(read, write *sql.DB) *SplitDB {
	return &SplitDB{read: read, write: write}
}

func (s *SplitDB) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	// sqlc emits ExecContext only for :exec statements, which are always writes.
	return s.write.ExecContext(ctx, query, args...)
}

func (s *SplitDB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	// Conservative: prepared statements may write; route to the writer. sqlc's
	// default codegen does not call PrepareContext, so this is effectively unused.
	return s.write.PrepareContext(ctx, query)
}

func (s *SplitDB) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	if isWriteQuery(query) {
		return s.write.QueryContext(ctx, query, args...)
	}
	return s.read.QueryContext(ctx, query, args...)
}

func (s *SplitDB) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	if isWriteQuery(query) {
		return s.write.QueryRowContext(ctx, query, args...)
	}
	return s.read.QueryRowContext(ctx, query, args...)
}

// isWriteQuery reports whether a statement mutates data, by its leading SQL
// keyword. sqlc routes both SELECTs and INSERT/UPDATE/DELETE...RETURNING
// through QueryContext/QueryRowContext (the latter are :one/:many with a
// RETURNING clause), so the DBTX method alone can't separate reads from
// writes — the verb can. The leading-token scan first strips sqlc's
// `-- name: X :one` comment lines. Verified against the generated query set:
// none begins with a WITH CTE, so first-token classification is exhaustive
// (a guard test pins this).
func isWriteQuery(query string) bool {
	s := strings.TrimLeft(query, " \t\r\n")
	for strings.HasPrefix(s, "--") {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			return false // comment-only string: no statement, treat as read
		}
		s = strings.TrimLeft(s[i+1:], " \t\r\n")
	}
	end := strings.IndexAny(s, " \t\r\n(")
	if end < 0 {
		end = len(s)
	}
	switch strings.ToUpper(s[:end]) {
	case "INSERT", "UPDATE", "DELETE", "REPLACE":
		return true
	default:
		return false
	}
}
