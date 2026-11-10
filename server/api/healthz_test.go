// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHealthzFollowsReadiness(t *testing.T) {
	var ready atomic.Bool
	h := HealthzHandler(ready.Load)

	get := func() (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		return rec.Code, rec.Body.String()
	}

	if code, body := get(); code != http.StatusServiceUnavailable || body != "unavailable\n" {
		t.Fatalf("starting: got %d %q, want 503 unavailable", code, body)
	}
	ready.Store(true)
	if code, body := get(); code != http.StatusOK || body != "ok\n" {
		t.Fatalf("ready: got %d %q, want 200 ok", code, body)
	}
	ready.Store(false)
	if code, _ := get(); code != http.StatusServiceUnavailable {
		t.Fatalf("stopping: got %d, want 503", code)
	}
}
