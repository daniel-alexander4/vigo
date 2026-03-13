// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	isgrpc "github.com/hobnob/hobnob/server/grpc"
)

type stubQueryDispatcher struct {
	lastTraitPath string
	lastTarget    string
	result        *isgrpc.LiveQueryResult
	err           error
}

func (s *stubQueryDispatcher) DispatchQuery(_ context.Context, traitPath, targetSpec string, _ time.Duration) (*isgrpc.LiveQueryResult, error) {
	s.lastTraitPath = traitPath
	s.lastTarget = targetSpec
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

func TestQueryHandler_Success(t *testing.T) {
	dispatcher := &stubQueryDispatcher{
		result: &isgrpc.LiveQueryResult{
			QueryID:   "q-1",
			TraitPath: "os.distro",
			Responded: []isgrpc.QueryEntry{
				{Hostname: "web01", Value: "ubuntu", Found: true, Status: "responded"},
			},
			Offline: []isgrpc.QueryEntry{
				{Hostname: "web02", Value: "debian", Found: true, Status: "offline", CollectedAt: "2026-03-11T10:00:00Z", Stale: true},
			},
			Counts: isgrpc.QueryCounts{Responded: 1, Offline: 1},
		},
	}
	handler := NewQueryHandler(dispatcher)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"traitPath":"os.distro","target":"*.web.*"}`
	req := httptest.NewRequest("POST", "/api/v1/query", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp isgrpc.LiveQueryResult
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.TraitPath != "os.distro" {
		t.Errorf("expected os.distro, got %s", resp.TraitPath)
	}
	if resp.Counts.Responded != 1 {
		t.Errorf("expected 1 responded, got %d", resp.Counts.Responded)
	}
	if resp.Counts.Offline != 1 {
		t.Errorf("expected 1 offline, got %d", resp.Counts.Offline)
	}
	if len(resp.Responded) != 1 || resp.Responded[0].Hostname != "web01" {
		t.Errorf("unexpected responded: %+v", resp.Responded)
	}
	if dispatcher.lastTraitPath != "os.distro" {
		t.Errorf("expected traitPath os.distro, got %s", dispatcher.lastTraitPath)
	}
}

func TestQueryHandler_MissingTraitPath(t *testing.T) {
	handler := NewQueryHandler(&stubQueryDispatcher{})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"target":"*.web.*"}`
	req := httptest.NewRequest("POST", "/api/v1/query", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestQueryHandler_MissingTarget(t *testing.T) {
	handler := NewQueryHandler(&stubQueryDispatcher{})
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"traitPath":"os.distro"}`
	req := httptest.NewRequest("POST", "/api/v1/query", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestQueryHandler_DispatchError(t *testing.T) {
	dispatcher := &stubQueryDispatcher{err: fmt.Errorf("no thralls matched")}
	handler := NewQueryHandler(dispatcher)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	body := `{"traitPath":"os.distro","target":"nonexistent"}`
	req := httptest.NewRequest("POST", "/api/v1/query", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}
