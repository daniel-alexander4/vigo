// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAI_MissingQuestion(t *testing.T) {
	// We can test error paths without a real AI assistant by testing with a nil
	// assistant — the handler checks the question field before calling Ask.
	handler := NewAIHandler(nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"question":""}`
	req := httptest.NewRequest("POST", "/api/v1/ask", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty question, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAI_InvalidRequestBody(t *testing.T) {
	handler := NewAIHandler(nil)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `not json at all`
	req := httptest.NewRequest("POST", "/api/v1/ask", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, adminReq(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid body, got %d", rec.Code)
	}
}
