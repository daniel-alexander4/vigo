// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import "testing"

// TestGoosGoarchFromTraits verifies the trait extraction used by the
// auto-update path to look up the correct per-arch server version. This is
// the helper that prevents the cross-arch update loop: passing the wrong
// (goos, goarch) pair would either disable auto-update for valid arches or
// re-introduce the loop. Each case was chosen to exercise a real envoy in
// Dan's fleet or a near-miss.
func TestGoosGoarchFromTraits(t *testing.T) {
	cases := []struct {
		name      string
		traits    string
		wantGoos  string
		wantGoarc string
	}{
		{
			name:      "linux_arm64_raspberrypi",
			traits:    `{"os":{"family":"debian","arch":"arm64","version":"12"}}`,
			wantGoos:  "linux",
			wantGoarc: "arm64",
		},
		{
			name:      "linux_amd64_ubuntu",
			traits:    `{"os":{"family":"debian","arch":"amd64"}}`,
			wantGoos:  "linux",
			wantGoarc: "amd64",
		},
		{
			name:      "linux_amd64_rhel",
			traits:    `{"os":{"family":"rhel","arch":"amd64"}}`,
			wantGoos:  "linux",
			wantGoarc: "amd64",
		},
		{
			name:      "macos_arm64",
			traits:    `{"os":{"family":"darwin","arch":"arm64"}}`,
			wantGoos:  "darwin",
			wantGoarc: "arm64",
		},
		{
			name:      "windows_amd64",
			traits:    `{"os":{"family":"windows","arch":"amd64"}}`,
			wantGoos:  "windows",
			wantGoarc: "amd64",
		},
		{
			name:      "freebsd_passthrough",
			traits:    `{"os":{"family":"freebsd","arch":"amd64"}}`,
			wantGoos:  "freebsd",
			wantGoarc: "amd64",
		},
		{
			name:      "missing_arch_returns_empty",
			traits:    `{"os":{"family":"debian"}}`,
			wantGoos:  "",
			wantGoarc: "",
		},
		{
			name:      "missing_family_returns_empty",
			traits:    `{"os":{"arch":"arm64"}}`,
			wantGoos:  "",
			wantGoarc: "",
		},
		{
			name:      "empty_traits",
			traits:    "",
			wantGoos:  "",
			wantGoarc: "",
		},
		{
			name:      "malformed_json",
			traits:    `not json`,
			wantGoos:  "",
			wantGoarc: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotGoos, gotGoarc := goosGoarchFromTraits([]byte(tc.traits))
			if gotGoos != tc.wantGoos || gotGoarc != tc.wantGoarc {
				t.Errorf("goosGoarchFromTraits(%q) = (%q, %q), want (%q, %q)",
					tc.traits, gotGoos, gotGoarc, tc.wantGoos, tc.wantGoarc)
			}
		})
	}
}
