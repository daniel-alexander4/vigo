// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

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
//
// The tail directives are not covered by default-src and close what it
// leaves open: no <base> rewriting of relative URLs, forms post only to
// this origin (every form and every logout redirect is same-origin), no
// plugin content, and framing only by this origin (the CSP twin of
// X-Frame-Options: SAMEORIGIN).
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; " + cspTail

const cspTail = "base-uri 'none'; form-action 'self'; object-src 'none'; frame-ancestors 'self'"

// IsowebauthLoginCSP is CSP plus one connect-src origin, for the isowebauth
// login page ONLY: its script (/static/js/isowebauth.js) asks the local
// isowebauth signing app on 127.0.0.1:7890 to sign the login challenge,
// which the locked policy's connect-src (inherited from default-src 'self')
// blocks. Every other HTML surface keeps CSP.
const IsowebauthLoginCSP = "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self' " + IsowebauthSignerOrigin + "; " + cspTail

// IsowebauthSignerOrigin is the local signing app's origin the isowebauth
// login script fetches; it must match ui/static/js/isowebauth.js.
const IsowebauthSignerOrigin = "http://127.0.0.1:7890"

// Headers sets the standard security response headers on an HTML response:
// the locked CSP plus nosniff, clickjacking, and referrer protections. Call
// it from every handler that writes an HTML document.
func Headers(w http.ResponseWriter) {
	HeadersWithCSP(w, CSP)
}

// HeadersWithCSP is Headers with an explicit policy. Only the constants in
// this package may be passed, so every policy stays defined here.
func HeadersWithCSP(w http.ResponseWriter, csp string) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("Content-Security-Policy", csp)
}
