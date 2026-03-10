package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// mockExecDBTX implements DBTX, capturing Exec calls and optionally returning errors.
type mockExecDBTX struct {
	execFn func(ctx context.Context, sql string, args ...any) (sql.Result, error)
}

func (m *mockExecDBTX) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return m.execFn(ctx, query, args...)
}

func (m *mockExecDBTX) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	return nil, nil
}

func (m *mockExecDBTX) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, nil
}

func (m *mockExecDBTX) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

// mockResult implements sql.Result for tests.
type mockResult struct {
	rowsAffected int64
}

func (r mockResult) LastInsertId() (int64, error) { return 0, nil }
func (r mockResult) RowsAffected() (int64, error) { return r.rowsAffected, nil }

func TestLastSeenBatcher_FlushSuccess(t *testing.T) {
	var execCalled int
	mock := &mockExecDBTX{
		execFn: func(_ context.Context, _ string, _ ...any) (sql.Result, error) {
			execCalled++
			return mockResult{rowsAffected: 2}, nil
		},
	}

	b := NewLastSeenBatcher(New(mock))

	id1, id2 := uuid.New().String(), uuid.New().String()
	b.Mark(id1)
	b.Mark(id2)
	b.Flush(context.Background())

	if execCalled != 1 {
		t.Fatalf("expected 1 exec call, got %d", execCalled)
	}

	// After successful flush, pending should be empty — second flush is a no-op.
	b.Flush(context.Background())
	if execCalled != 1 {
		t.Fatalf("expected no additional exec call, got %d", execCalled)
	}
}

func TestLastSeenBatcher_FlushError_RetriesIDs(t *testing.T) {
	callCount := 0
	dbErr := errors.New("connection refused")
	mock := &mockExecDBTX{
		execFn: func(_ context.Context, _ string, _ ...any) (sql.Result, error) {
			callCount++
			if callCount == 1 {
				return nil, dbErr
			}
			return mockResult{rowsAffected: 2}, nil
		},
	}

	b := NewLastSeenBatcher(New(mock))

	id1, id2 := uuid.New().String(), uuid.New().String()
	b.Mark(id1)
	b.Mark(id2)

	// First flush fails — IDs should be re-added.
	b.Flush(context.Background())
	if callCount != 1 {
		t.Fatalf("expected 1 exec call, got %d", callCount)
	}

	// Second flush retries the same IDs.
	b.Flush(context.Background())
	if callCount != 2 {
		t.Fatalf("expected 2 exec calls, got %d", callCount)
	}

	// Third flush is a no-op (success on call 2 cleared pending).
	b.Flush(context.Background())
	if callCount != 2 {
		t.Fatalf("expected no additional exec call, got %d total", callCount)
	}
}

func TestLastSeenBatcher_EmptyFlush(t *testing.T) {
	mock := &mockExecDBTX{
		execFn: func(_ context.Context, _ string, _ ...any) (sql.Result, error) {
			t.Fatal("exec should not be called on empty flush")
			return nil, nil
		},
	}

	b := NewLastSeenBatcher(New(mock))
	b.Flush(context.Background())
}
