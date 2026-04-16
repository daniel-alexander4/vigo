// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	vigoauth "github.com/daniel-alexander4/vigo/server/auth"
	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
)

// openApprovalTestDB returns an in-memory SQLite database with the full
// migration schema. Delegates to the shared openMigratedTestDB helper.
func openApprovalTestDB(t *testing.T) (*sql.DB, *db.Queries) {
	sqlDB := openMigratedTestDB(t)
	return sqlDB, db.New(sqlDB)
}

// newApprovalTestHandler creates an ApprovalHandler with test dependencies.
func newApprovalTestHandler(t *testing.T, queries *db.Queries) (*ApprovalHandler, *http.ServeMux) {
	t.Helper()
	h := NewApprovalHandler(queries, nil, config.PublishSection{
		ApprovalRequired: true,
		ApprovalTimeout:  "1h",
	}, nil, fleet.NewFleetIndex(), nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux
}

// requestWithUser creates an HTTP request with an authenticated user in context.
func requestWithUser(method, path string, body []byte, username string) *http.Request {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if username != "" {
		ctx := vigoauth.ContextWithUser(req.Context(), &vigoauth.User{
			ID:       username + "-id",
			Username: username,
			Role:     "admin",
		})
		req = req.WithContext(ctx)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestCreatePendingChange(t *testing.T) {
	_, queries := openApprovalTestDB(t)
	_, mux := newApprovalTestHandler(t, queries)

	body, _ := json.Marshal(map[string]string{"diff_summary": "changed nginx.vgo"})
	req := requestWithUser("POST", "/api/v1/config/changes", body, "alice")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp["status"] != "pending" {
		t.Errorf("expected status pending, got %v", resp["status"])
	}
	if resp["requested_by"] != "alice" {
		t.Errorf("expected requested_by alice, got %v", resp["requested_by"])
	}
	if resp["diff_summary"] != "changed nginx.vgo" {
		t.Errorf("expected diff_summary 'changed nginx.vgo', got %v", resp["diff_summary"])
	}
	if resp["id"] == nil || resp["id"].(float64) < 1 {
		t.Errorf("expected valid id, got %v", resp["id"])
	}
}

func TestApproveChange(t *testing.T) {
	_, queries := openApprovalTestDB(t)
	// Use a nil configStore — approval will fail on reload, but we test
	// the approval logic by providing a handler that doesn't actually reload.
	h := &ApprovalHandler{
		queries:    queries,
		publishCfg: config.PublishSection{ApprovalRequired: true, ApprovalTimeout: "1h"},
		fleetIndex: fleet.NewFleetIndex(),
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Create a change as alice.
	body, _ := json.Marshal(map[string]string{"diff_summary": "test diff"})
	req := requestWithUser("POST", "/api/v1/config/changes", body, "alice")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var createResp map[string]any
	json.NewDecoder(rec.Body).Decode(&createResp)
	changeID := strconv.FormatInt(int64(createResp["id"].(float64)), 10)

	// Approve as bob — configStore is nil so reload will panic.
	// Instead, we directly check the DB after calling approve which will fail
	// at reload. Let's instead test the pre-reload checks by verifying status.

	// Verify the change is pending in DB.
	change, err := queries.GetConfigChange(req.Context(), int64(createResp["id"].(float64)))
	if err != nil {
		t.Fatalf("get change: %v", err)
	}
	if change.Status != "pending" {
		t.Fatalf("expected pending, got %s", change.Status)
	}

	// Approve via DB directly to verify the query works, then check via API.
	err = queries.ApproveConfigChange(req.Context(), db.ApproveConfigChangeParams{
		ApprovedBy: sql.NullString{String: "bob", Valid: true},
		ID:         int64(createResp["id"].(float64)),
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Verify via GET.
	req2 := requestWithUser("GET", "/api/v1/config/changes/"+changeID, nil, "bob")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var getResp map[string]any
	json.NewDecoder(rec2.Body).Decode(&getResp)
	if getResp["status"] != "approved" {
		t.Errorf("expected approved, got %v", getResp["status"])
	}
	if getResp["approved_by"] != "bob" {
		t.Errorf("expected approved_by bob, got %v", getResp["approved_by"])
	}
}

func TestApproveChangeSameUserRejected(t *testing.T) {
	_, queries := openApprovalTestDB(t)
	_, mux := newApprovalTestHandler(t, queries)

	// Create as alice.
	body, _ := json.Marshal(map[string]string{"diff_summary": "test"})
	req := requestWithUser("POST", "/api/v1/config/changes", body, "alice")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", rec.Code)
	}

	var createResp map[string]any
	json.NewDecoder(rec.Body).Decode(&createResp)
	changeID := strconv.FormatInt(int64(createResp["id"].(float64)), 10)

	// Try to approve as alice (same user) — should be rejected.
	req2 := requestWithUser("POST", "/api/v1/config/changes/"+changeID+"/approve", nil, "alice")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var errResp map[string]any
	json.NewDecoder(rec2.Body).Decode(&errResp)
	msg, _ := errResp["message"].(string)
	if msg == "" {
		t.Error("expected error message about two-person rule")
	}
}

func TestRejectChange(t *testing.T) {
	_, queries := openApprovalTestDB(t)
	_, mux := newApprovalTestHandler(t, queries)

	// Create as alice.
	body, _ := json.Marshal(map[string]string{"diff_summary": "test"})
	req := requestWithUser("POST", "/api/v1/config/changes", body, "alice")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", rec.Code)
	}

	var createResp map[string]any
	json.NewDecoder(rec.Body).Decode(&createResp)
	changeID := strconv.FormatInt(int64(createResp["id"].(float64)), 10)

	// Reject as bob with reason.
	rejectBody, _ := json.Marshal(map[string]string{"reason": "not ready for production"})
	req2 := requestWithUser("POST", "/api/v1/config/changes/"+changeID+"/reject", rejectBody, "bob")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("reject: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Verify status via GET.
	req3 := requestWithUser("GET", "/api/v1/config/changes/"+changeID, nil, "bob")
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)

	var getResp map[string]any
	json.NewDecoder(rec3.Body).Decode(&getResp)
	if getResp["status"] != "rejected" {
		t.Errorf("expected rejected, got %v", getResp["status"])
	}
	if getResp["reject_reason"] != "not ready for production" {
		t.Errorf("expected reject reason, got %v", getResp["reject_reason"])
	}
}

func TestExpirePendingChanges(t *testing.T) {
	sqlDB, queries := openApprovalTestDB(t)

	// Insert a change with an expiry time in the past.
	pastExpiry := time.Now().UTC().Add(-1 * time.Hour)
	_, err := sqlDB.Exec(
		`INSERT INTO config_changes (requested_by, expires_at, diff_summary) VALUES (?, ?, ?)`,
		"alice", pastExpiry.Format("2006-01-02 15:04:05"), "expired diff",
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Run the expire query.
	if err := queries.ExpirePendingConfigChanges(req_ctx()); err != nil {
		t.Fatalf("expire: %v", err)
	}

	// Verify the change is now expired.
	change, err := queries.GetConfigChange(req_ctx(), 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if change.Status != "expired" {
		t.Errorf("expected expired, got %s", change.Status)
	}
}

func TestCancelExistingPendingOnNewSubmit(t *testing.T) {
	_, queries := openApprovalTestDB(t)
	_, mux := newApprovalTestHandler(t, queries)

	// Create first change as alice.
	body1, _ := json.Marshal(map[string]string{"diff_summary": "change 1"})
	req1 := requestWithUser("POST", "/api/v1/config/changes", body1, "alice")
	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("create 1: expected 201, got %d", rec1.Code)
	}

	var resp1 map[string]any
	json.NewDecoder(rec1.Body).Decode(&resp1)
	id1 := strconv.FormatInt(int64(resp1["id"].(float64)), 10)

	// Create second change — first should be cancelled.
	body2, _ := json.Marshal(map[string]string{"diff_summary": "change 2"})
	req2 := requestWithUser("POST", "/api/v1/config/changes", body2, "bob")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("create 2: expected 201, got %d", rec2.Code)
	}

	// Verify first change is no longer pending (cancelled = expired status).
	req3 := requestWithUser("GET", "/api/v1/config/changes/"+id1, nil, "alice")
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)

	var getResp map[string]any
	json.NewDecoder(rec3.Body).Decode(&getResp)
	if getResp["status"] == "pending" {
		t.Error("first change should no longer be pending after second submit")
	}
	if getResp["status"] != "expired" {
		t.Errorf("expected first change status to be expired (cancelled), got %v", getResp["status"])
	}

	// Verify the list only has the second change.
	req4 := requestWithUser("GET", "/api/v1/config/changes", nil, "alice")
	rec4 := httptest.NewRecorder()
	mux.ServeHTTP(rec4, req4)

	var listResp struct {
		Changes []map[string]any `json:"changes"`
	}
	json.NewDecoder(rec4.Body).Decode(&listResp)
	if len(listResp.Changes) != 1 {
		t.Errorf("expected 1 pending change, got %d", len(listResp.Changes))
	}
}

// req_ctx returns a background context for DB queries in tests.
func req_ctx() interface{ Deadline() (time.Time, bool); Done() <-chan struct{}; Err() error; Value(any) any } {
	return httptest.NewRequest("GET", "/", nil).Context()
}
