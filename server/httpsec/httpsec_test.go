// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package httpsec

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func directives(csp string) map[string]string {
	out := map[string]string{}
	for _, d := range strings.Split(csp, ";") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		name, val, _ := strings.Cut(d, " ")
		out[name] = val
	}
	return out
}

// TestCSP_LockedDirectives pins the locked policy, including the
// directives default-src does not cover.
func TestCSP_LockedDirectives(t *testing.T) {
	want := map[string]string{
		"default-src":     "'self'",
		"script-src":      "'self'",
		"style-src":       "'self'",
		"base-uri":        "'none'",
		"form-action":     "'self'",
		"object-src":      "'none'",
		"frame-ancestors": "'self'",
	}
	for _, csp := range []string{CSP, IsowebauthLoginCSP} {
		got := directives(csp)
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%q: %s = %q, want %q", csp, k, got[k], v)
			}
		}
		if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
			t.Errorf("policy must stay locked: %s", csp)
		}
	}
	if _, ok := directives(CSP)["connect-src"]; ok {
		t.Error("locked CSP must not widen connect-src")
	}
	if got := directives(IsowebauthLoginCSP)["connect-src"]; got != "'self' "+IsowebauthSignerOrigin {
		t.Errorf("isowebauth connect-src = %q", got)
	}
}

// TestIsowebauthSignerOrigin_MatchesScript: the origin the login script
// fetches must be the one the login CSP allows, or login silently fails.
func TestIsowebauthSignerOrigin_MatchesScript(t *testing.T) {
	js, err := os.ReadFile(filepath.Join("..", "..", "ui", "static", "js", "isowebauth.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), "fetch('"+IsowebauthSignerOrigin+"/") {
		t.Errorf("isowebauth.js does not fetch %s — update IsowebauthSignerOrigin", IsowebauthSignerOrigin)
	}
}

func TestRedactURLError(t *testing.T) {
	const tok = "TOKEN123"
	_, err := url.Parse("http://[::1/x/" + tok)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	wrapped := fmt.Errorf("outer: %w", err)
	got := RedactURLError(wrapped)
	if strings.Contains(got.Error(), tok) {
		t.Errorf("RedactURLError kept the URL: %v", got)
	}
	plain := errors.New("plain")
	if RedactURLError(plain) != plain {
		t.Error("non-url error must pass through unchanged")
	}
	if h := URLHost("https://hooks.slack.com/services/" + tok); h != "hooks.slack.com" {
		t.Errorf("URLHost = %q", h)
	}
	if h := URLHost("http://[::1/" + tok); strings.Contains(h, tok) {
		t.Errorf("URLHost echoed an invalid input: %q", h)
	}
}

func TestHeaders_SetsLockedCSP(t *testing.T) {
	rec := httptest.NewRecorder()
	Headers(rec)
	if got := rec.Header().Get("Content-Security-Policy"); got != CSP {
		t.Errorf("Headers CSP = %q", got)
	}
	if rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Error("X-Frame-Options missing")
	}
}
