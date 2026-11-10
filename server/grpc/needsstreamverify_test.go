// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import "testing"

// TestNeedsStreamVerify pins the held-stream signature-skip rule: in the
// default mode every message is verified; in handshake-only mode only the
// stream owner's OWN messages are skipped, while a foreign envoy_id (a proxied
// device result) is always verified — that branch is load-bearing and must
// never be skipped regardless of mode.
func TestNeedsStreamVerify(t *testing.T) {
	const owner = "envoy-A"
	const foreign = "device-virtual-B"

	cases := []struct {
		name     string
		relax    bool
		msgEnvoy string
		want     bool
	}{
		{name: "default verifies own", relax: false, msgEnvoy: owner, want: true},
		{name: "default verifies foreign", relax: false, msgEnvoy: foreign, want: true},
		{name: "relaxed skips own", relax: true, msgEnvoy: owner, want: false},
		{name: "relaxed still verifies foreign", relax: true, msgEnvoy: foreign, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsStreamVerify(tc.relax, tc.msgEnvoy, owner); got != tc.want {
				t.Errorf("needsStreamVerify(relax=%v, %q, %q) = %v, want %v",
					tc.relax, tc.msgEnvoy, owner, got, tc.want)
			}
		})
	}
}
