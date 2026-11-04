// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

// TestBootstrapCommandCarriesToken: the served script takes the token only
// from the ?token= query parameter (server/bootstrap BootstrapScript), so the
// printed one-liner must put it there; a loopback --server is replaced by a
// placeholder because it is meaningless on the new envoy.
func TestBootstrapCommandCarriesToken(t *testing.T) {
	cases := []struct {
		base, wantURL string
		wantNote      bool
	}{
		{"https://vigo.example.com:8443", `"https://vigo.example.com:8443/bootstrap?token=abc123"`, false},
		{"https://vigo.example.com:8443/", `"https://vigo.example.com:8443/bootstrap?token=abc123"`, false},
		{"https://localhost:8443", `"https://<server-address>:8443/bootstrap?token=abc123"`, true},
		{"https://127.0.0.1:9443", `"https://<server-address>:9443/bootstrap?token=abc123"`, true},
	}
	for _, c := range cases {
		cmd, note := bootstrapCommand(c.base, "abc123")
		if want := "curl -sSfk " + c.wantURL + " | sudo sh"; cmd != want {
			t.Errorf("bootstrapCommand(%q) = %q, want %q", c.base, cmd, want)
		}
		if strings.Contains(cmd, "VIGO_TOKEN") {
			t.Errorf("bootstrapCommand(%q) uses VIGO_TOKEN, which the bootstrap script never reads", c.base)
		}
		if (note != "") != c.wantNote {
			t.Errorf("bootstrapCommand(%q) note=%q, wantNote=%v", c.base, note, c.wantNote)
		}
	}
}
