// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package db

import (
	"context"
	"database/sql"
	"strings"
	"strconv"
)

// PgDBTX wraps a *sql.DB and rewrites SQLite-style ? placeholders to
// Postgres-style $1, $2, ... so that sqlc-generated code works with both
// drivers without separate codegen.
type PgDBTX struct {
	DB *sql.DB
}

func (p *PgDBTX) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return p.DB.ExecContext(ctx, rewritePlaceholders(query), args...)
}

func (p *PgDBTX) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.DB.PrepareContext(ctx, rewritePlaceholders(query))
}

func (p *PgDBTX) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return p.DB.QueryContext(ctx, rewritePlaceholders(query), args...)
}

func (p *PgDBTX) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return p.DB.QueryRowContext(ctx, rewritePlaceholders(query), args...)
}

// rewritePlaceholders replaces ? with $1, $2, $3, ... for Postgres.
// It skips ? inside single-quoted string literals.
func rewritePlaceholders(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}

	var buf strings.Builder
	buf.Grow(len(query) + 16)
	n := 0
	inString := false

	for i := 0; i < len(query); i++ {
		ch := query[i]
		if ch == '\'' {
			inString = !inString
			buf.WriteByte(ch)
		} else if ch == '?' && !inString {
			n++
			buf.WriteByte('$')
			buf.WriteString(strconv.Itoa(n))
		} else {
			buf.WriteByte(ch)
		}
	}
	return buf.String()
}
