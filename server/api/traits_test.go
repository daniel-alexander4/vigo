package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// mockDBTX implements db.DBTX for testing the traits handler.
type mockDBTX struct {
	queryRowFn func(sql string, args ...any) pgx.Row
}

func (m *mockDBTX) Exec(_ context.Context, _ string, _ ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (m *mockDBTX) Query(_ context.Context, _ string, _ ...interface{}) (pgx.Rows, error) {
	return nil, nil
}

func (m *mockDBTX) QueryRow(_ context.Context, sql string, args ...interface{}) pgx.Row {
	return m.queryRowFn(sql, args...)
}

// mockRow implements pgx.Row, returning preset values or an error.
type mockRow struct {
	scanFn func(dest ...any) error
}

func (r *mockRow) Scan(dest ...any) error {
	return r.scanFn(dest...)
}

func TestTraitsHandler_Success(t *testing.T) {
	// Mock: GetNodeByHostname returns a client, GetLatestNodeTraits returns traits.
	callCount := 0
	collectedAt := time.Date(2026, 3, 7, 10, 30, 0, 0, time.UTC)
	mockDB := &mockDBTX{
		queryRowFn: func(sql string, args ...any) pgx.Row {
			callCount++
			if callCount == 1 {
				// GetNodeByHostname
				return &mockRow{scanFn: func(dest ...any) error {
					// id, hostname, ip_address, pubkey_pem, enrolled_at, last_seen, revoked, revoked_at, force_push
					*dest[1].(*string) = "web01"
					return nil
				}}
			}
			// GetLatestNodeTraits
			return &mockRow{scanFn: func(dest ...any) error {
				// id, client_id, traits, collected_at
				*dest[2].(*json.RawMessage) = json.RawMessage(`{"os":{"distro":"ubuntu","version":"22.04"}}`)
				*dest[3].(*time.Time) = collectedAt
				return nil
			}}
		},
	}

	handler := newTraitsHandlerWithDB(mockDB)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/nodes/web01/traits", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}

	var resp struct {
		Hostname    string          `json:"hostname"`
		CollectedAt string          `json:"collected_at"`
		Traits       json.RawMessage `json:"traits"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Hostname != "web01" {
		t.Errorf("expected hostname web01, got %s", resp.Hostname)
	}

	// collected_at must be RFC3339
	if _, err := time.Parse(time.RFC3339, resp.CollectedAt); err != nil {
		t.Errorf("collected_at is not RFC3339: %s", resp.CollectedAt)
	}

	if resp.CollectedAt != "2026-03-07T10:30:00Z" {
		t.Errorf("expected collected_at 2026-03-07T10:30:00Z, got %s", resp.CollectedAt)
	}

	// traits must be the raw JSON (not double-encoded)
	var traits map[string]any
	if err := json.Unmarshal(resp.Traits, &traits); err != nil {
		t.Fatalf("traits is not valid JSON: %v", err)
	}

	osMap, ok := traits["os"].(map[string]any)
	if !ok {
		t.Fatalf("expected os to be a map, got %T", traits["os"])
	}
	if osMap["distro"] != "ubuntu" {
		t.Errorf("expected os.distro=ubuntu, got %v", osMap["distro"])
	}
}

func TestTraitsHandler_NodeNotFound(t *testing.T) {
	mockDB := &mockDBTX{
		queryRowFn: func(sql string, args ...any) pgx.Row {
			return &mockRow{scanFn: func(dest ...any) error {
				return pgx.ErrNoRows
			}}
		},
	}

	handler := newTraitsHandlerWithDB(mockDB)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/nodes/nonexistent/traits", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["error"] != "node 'nonexistent' not found" {
		t.Errorf("expected node not found error, got %q", resp["error"])
	}
}

func TestTraitsHandler_NoFacts(t *testing.T) {
	callCount := 0
	mockDB := &mockDBTX{
		queryRowFn: func(sql string, args ...any) pgx.Row {
			callCount++
			if callCount == 1 {
				// GetNodeByHostname succeeds
				return &mockRow{scanFn: func(dest ...any) error {
					*dest[1].(*string) = "web01"
					return nil
				}}
			}
			// GetLatestNodeTraits returns no rows
			return &mockRow{scanFn: func(dest ...any) error {
				return pgx.ErrNoRows
			}}
		},
	}

	handler := newTraitsHandlerWithDB(mockDB)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/nodes/web01/traits", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	expected := "no traits available for 'web01' -- node has not reported traits yet"
	if resp["error"] != expected {
		t.Errorf("expected %q, got %q", expected, resp["error"])
	}
}
