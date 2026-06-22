// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
			s := &AgentService{relaxStreamSig: tc.relax}
			if got := s.needsStreamVerify(tc.msgEnvoy, owner); got != tc.want {
				t.Errorf("needsStreamVerify(%q, %q) relax=%v = %v, want %v",
					tc.msgEnvoy, owner, tc.relax, got, tc.want)
			}
		})
	}
}
