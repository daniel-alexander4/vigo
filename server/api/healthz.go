// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import "net/http"

// HealthzHandler answers GET /healthz with 200 "ok" once the server has
// finished starting, and 503 "unavailable" before that and while it shuts
// down. It is unauthenticated and says nothing else: no version, no
// subsystem detail, nothing a scanner can use. Load balancers, the Docker
// healthcheck and the install check read the status code.
func HealthzHandler(ready func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if !ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("unavailable\n"))
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
}
