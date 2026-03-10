package db

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// mockExecDBTX implements DBTX, capturing Exec calls and optionally returning errors.
type mockExecDBTX struct {
	execFn func(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (m *mockExecDBTX) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return m.execFn(ctx, sql, args...)
}

func (m *mockExecDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (m *mockExecDBTX) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

func TestLastSeenBatcher_FlushSuccess(t *testing.T) {
	var execCalled int
	mock := &mockExecDBTX{
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			execCalled++
			return pgconn.NewCommandTag("UPDATE 2"), nil
		},
	}

	b := NewLastSeenBatcher(New(mock))

	id1, id2 := uuid.New(), uuid.New()
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
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			callCount++
			if callCount == 1 {
				return pgconn.CommandTag{}, dbErr
			}
			return pgconn.NewCommandTag("UPDATE 2"), nil
		},
	}

	b := NewLastSeenBatcher(New(mock))

	id1, id2 := uuid.New(), uuid.New()
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
		execFn: func(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			t.Fatal("exec should not be called on empty flush")
			return pgconn.CommandTag{}, nil
		},
	}

	b := NewLastSeenBatcher(New(mock))
	b.Flush(context.Background())
}
