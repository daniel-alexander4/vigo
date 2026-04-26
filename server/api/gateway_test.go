// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// echoHandler reads the entire request body and writes it back.
// Used to verify whether the body was fully readable or truncated by the middleware.
func echoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	})
}

func TestMaxBodyMiddleware_SmallBody(t *testing.T) {
	handler := MaxBodyMiddleware(echoHandler())

	body := bytes.Repeat([]byte("x"), 100)
	req := httptest.NewRequest("POST", "/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 100 {
		t.Fatalf("expected 100-byte filesync, got %d bytes", rec.Body.Len())
	}
}

func TestMaxBodyMiddleware_ExactLimit(t *testing.T) {
	handler := MaxBodyMiddleware(echoHandler())

	body := bytes.Repeat([]byte("a"), maxRequestBodySize)
	req := httptest.NewRequest("POST", "/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != maxRequestBodySize {
		t.Fatalf("expected %d-byte filesync, got %d bytes", maxRequestBodySize, rec.Body.Len())
	}
}

func TestMaxBodyMiddleware_OversizedBody(t *testing.T) {
	handler := MaxBodyMiddleware(echoHandler())

	// 1 byte over the limit.
	body := bytes.Repeat([]byte("b"), maxRequestBodySize+1)
	req := httptest.NewRequest("POST", "/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// The handler calls io.ReadAll which hits the MaxBytesReader limit.
	// MaxBytesReader causes the read to fail, and our filesync handler returns 413.
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
}
