// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTryPeerAuth covers the path × origin matrix that gates the synthetic
// loopback admin grant. The /ws/* prefix was added in 0.52.3 to fix the
// scrier WebSocket dial from `vigocli scrier ssh` — the CLI's REST calls
// were already getting peer-auth (under /api/v1/*) but the subsequent
// WebSocket upgrade was redirecting to /login.
func TestTryPeerAuth(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		remoteAddr string
		wantUser   bool
	}{
		{
			name:       "api path from loopback grants admin",
			path:       "/api/v1/envoys",
			remoteAddr: "127.0.0.1:34660",
			wantUser:   true,
		},
		{
			name:       "ws path from loopback grants admin (regression for 0.52.3 scrier dial)",
			path:       "/ws/scrier/abc-123",
			remoteAddr: "127.0.0.1:34660",
			wantUser:   true,
		},
		{
			name:       "ws path from docker bridge grants admin",
			path:       "/ws/scrier/abc-123",
			remoteAddr: "172.18.0.1:46554",
			wantUser:   true,
		},
		{
			name:       "ui path from loopback refused (must fall through to /login flow)",
			path:       "/login",
			remoteAddr: "127.0.0.1:34660",
			wantUser:   false,
		},
		{
			name:       "api path from non-loopback refused",
			path:       "/api/v1/envoys",
			remoteAddr: "203.0.113.5:9000",
			wantUser:   false,
		},
		{
			name:       "ws path from non-loopback refused",
			path:       "/ws/scrier/abc-123",
			remoteAddr: "203.0.113.5:9000",
			wantUser:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			req.RemoteAddr = tc.remoteAddr

			user := TryPeerAuth(req)
			if tc.wantUser {
				require.NotNil(t, user, "expected loopback admin, got nil")
				require.Equal(t, "admin", user.Role)
				require.Equal(t, "localhost", user.Username)
			} else {
				require.Nil(t, user, "expected nil, got %+v", user)
			}
		})
	}
}
