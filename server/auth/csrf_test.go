// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCrossOriginGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := CrossOriginGuard(ok)

	cases := []struct {
		name    string
		method  string
		path    string
		headers map[string]string
		want    int
	}{
		// Browser same-origin traffic: htmx posts, login, TOTP, logout.
		{"htmx post same-origin", "POST", "/api/v1/tasks", map[string]string{"Sec-Fetch-Site": "same-origin", "HX-Request": "true"}, 204},
		{"login same-origin", "POST", "/login", map[string]string{"Sec-Fetch-Site": "same-origin"}, 204},
		{"totp same-origin", "POST", "/totp", map[string]string{"Sec-Fetch-Site": "same-origin"}, 204},
		{"logout same-origin", "POST", "/logout", map[string]string{"Sec-Fetch-Site": "same-origin"}, 204},
		{"old browser matching origin", "POST", "/logout", map[string]string{"Origin": "https://vigo.example"}, 204},
		// Forged cross-origin requests.
		{"cross-site post", "POST", "/api/v1/tasks", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"same-site sibling post", "DELETE", "/api/v1/tokens/x", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		{"cross-site login", "POST", "/login", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"old browser foreign origin", "POST", "/api/v1/tasks", map[string]string{"Origin": "https://evil.example"}, 403},
		// Safe methods and non-browser clients pass.
		{"cross-site get", "GET", "/envoys", map[string]string{"Sec-Fetch-Site": "cross-site"}, 204},
		{"cli post no headers", "POST", "/api/v1/tasks", nil, 204},
		{"bearer post", "POST", "/api/v1/tasks", map[string]string{"Authorization": "Bearer vgot_x", "Sec-Fetch-Site": "cross-site"}, 204},
		{"agent route", "POST", "/api/v1/agent/report", map[string]string{"Sec-Fetch-Site": "cross-site"}, 204},
		{"bootstrap route", "POST", "/api/v1/bootstrap/register", map[string]string{"Sec-Fetch-Site": "cross-site"}, 204},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "https://vigo.example"+tc.path, nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
