// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"net/http"
	"strings"
)

// CrossOriginGuard rejects cross-origin state-changing browser requests
// (CSRF). It wraps the whole REST/UI listener, outside the auth middleware.
//
// The check is net/http's CrossOriginProtection: a POST/PUT/PATCH/DELETE is
// refused with 403 when the browser says it came from another site
// (Sec-Fetch-Site), or, on browsers without that header, when its Origin
// host is not the request's Host. Requests with neither header are not
// from a browser and pass, so vigocli, agents and curl are unaffected.
// GET/HEAD/OPTIONS always pass; they must stay side-effect free.
//
// SameSite=Lax on the session cookie already keeps cross-site POSTs from
// carrying the session in current browsers; this guard does not depend on
// that, and also covers same-site-but-cross-origin pages (a sibling
// subdomain) that Lax lets through.
//
// Bypassed:
//   - Bearer-token requests. The credential is a header a foreign page
//     cannot make a browser attach, so there is nothing to forge.
//   - Agent and bootstrap routes, which no browser session drives.
func CrossOriginGuard(next http.Handler) http.Handler {
	protected := http.NewCrossOriginProtection().Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ExtractBearerToken(r.Header.Get("Authorization")) != "" || csrfExemptPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		protected.ServeHTTP(w, r)
	})
}

func csrfExemptPath(path string) bool {
	return path == "/bootstrap" ||
		strings.HasPrefix(path, "/api/v1/agent/") ||
		strings.HasPrefix(path, "/api/v1/bootstrap/")
}
