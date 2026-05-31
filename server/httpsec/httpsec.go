// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package httpsec centralizes the HTTP security response headers — including
// the Content-Security-Policy — applied to every HTML surface Vigo serves:
// the web UI (pages + HTMX fragments) and the auth login / TOTP pages. The
// policy is defined in exactly one place here so the login page can never
// drift from the UI's policy.
package httpsec

import "net/http"

// CSP is the single Content-Security-Policy for all HTML responses. Fully
// locked: no 'unsafe-inline' and no nonce, for either scripts or styles —
// every script is external (/static/js, delegated via ui-actions.js) and
// every style is a class (/static/css + the u-*/w-pct-*/pct-*/cc-*/tgb-*
// utility layer). CSP nonces don't cover style *attributes*, so the only way
// to drop 'unsafe-inline' was to de-inline every style= attribute.
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'"

// Headers sets the standard security response headers on an HTML response:
// the locked CSP plus nosniff, clickjacking, and referrer protections. Call
// it from every handler that writes an HTML document.
func Headers(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("Content-Security-Policy", CSP)
}
