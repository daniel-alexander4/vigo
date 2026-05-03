// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteErrorEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusNotFound, "envoy not found")

	if got := rec.Code; got != http.StatusNotFound {
		t.Errorf("status code: got %d, want %d", got, http.StatusNotFound)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", got)
	}

	// The wire format must use the field name "message" — matches
	// grpc-gateway's error envelope so a single CLI parser handles
	// both gateway-routed and pure-REST handlers.
	var got APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response body: %v (body=%q)", err, rec.Body.String())
	}
	if got.Message != "envoy not found" {
		t.Errorf("Message: got %q, want %q", got.Message, "envoy not found")
	}

	// Sanity: the body must NOT include the legacy `error` field that
	// the (now-removed) jsonErr helper used. Pin this so a future
	// re-introduction of the divergent envelope shape is caught.
	if strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("response body contains legacy `error` field: %s", rec.Body.String())
	}
}
