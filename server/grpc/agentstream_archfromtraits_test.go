// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import "testing"

// TestGoosGoarchFromTraits verifies the goos/goarch extraction used by the
// auto-update path to look up the correct per-arch server version. This is
// the helper that prevents the cross-arch update loop: passing the wrong
// (goos, goarch) pair would either disable auto-update for valid arches or
// re-introduce the loop. It receives the pre-parsed "os" sub-tree of the
// inline traits (the caller's single Unmarshal in buildCheckInResponse). Each
// case was chosen to exercise a real envoy in Dan's fleet or a near-miss; an
// absent/malformed os sub-tree must fall back to empty.
func TestGoosGoarchFromTraits(t *testing.T) {
	cases := []struct {
		name      string
		os        string
		wantGoos  string
		wantGoarc string
	}{
		{
			name:      "linux_arm64_raspberrypi",
			os:        `{"family":"debian","arch":"arm64","version":"12"}`,
			wantGoos:  "linux",
			wantGoarc: "arm64",
		},
		{
			name:      "linux_amd64_ubuntu",
			os:        `{"family":"debian","arch":"amd64"}`,
			wantGoos:  "linux",
			wantGoarc: "amd64",
		},
		{
			name:      "linux_amd64_rhel",
			os:        `{"family":"rhel","arch":"amd64"}`,
			wantGoos:  "linux",
			wantGoarc: "amd64",
		},
		{
			name:      "macos_arm64",
			os:        `{"family":"darwin","arch":"arm64"}`,
			wantGoos:  "darwin",
			wantGoarc: "arm64",
		},
		{
			name:      "windows_amd64",
			os:        `{"family":"windows","arch":"amd64"}`,
			wantGoos:  "windows",
			wantGoarc: "amd64",
		},
		{
			name:      "freebsd_passthrough",
			os:        `{"family":"freebsd","arch":"amd64"}`,
			wantGoos:  "freebsd",
			wantGoarc: "amd64",
		},
		{
			name:      "missing_arch_returns_empty",
			os:        `{"family":"debian"}`,
			wantGoos:  "",
			wantGoarc: "",
		},
		{
			name:      "missing_family_returns_empty",
			os:        `{"arch":"arm64"}`,
			wantGoos:  "",
			wantGoarc: "",
		},
		{
			name:      "absent_os_subtree", // nil RawMessage from traitsTop["os"] when no "os" key
			os:        "",
			wantGoos:  "",
			wantGoarc: "",
		},
		{
			name:      "malformed_os", // e.g. fakefleet's os:"linux" string lands here as a bad sub-tree
			os:        `"linux"`,
			wantGoos:  "",
			wantGoarc: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotGoos, gotGoarc := goosGoarchFromTraits([]byte(tc.os))
			if gotGoos != tc.wantGoos || gotGoarc != tc.wantGoarc {
				t.Errorf("goosGoarchFromTraits(%q) = (%q, %q), want (%q, %q)",
					tc.os, gotGoos, gotGoarc, tc.wantGoos, tc.wantGoarc)
			}
		})
	}
}
