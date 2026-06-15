// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"
	"strings"
)

// communityPathsRule rejects community-uploaded modules that touch
// paths a portable third-party module has no business touching.
//
// Two blocklists, both gated on `opts.CommunityUpload` so in-fleet
// modules can do whatever the operator wants:
//
//	Kernel/runtime state — `/proc/`, `/sys/`, `/dev/`. A community
//	module reading or writing here is reaching into machine internals
//	that belong to the host OS or to higher-privilege management
//	tools. Reject.
//
//	Per-host territory — `/root/`, `/home/`, `/boot/`. User homes,
//	root's home, and boot media. A module pretending to install
//	software but actually planting `~/.ssh/authorized_keys` or
//	`/boot/grub/grub.cfg` would be a credential-takeover or
//	bootkit pattern; community modules don't need access to either.
//
// Applies to every string attribute, not just declared path-typed
// ones — operators can stick a path into any field, and we'd rather
// over-reject in community mode than miss a planted backdoor. Rooted
// paths only: `/proc/...` is checked, but `proc/...` (relative) is
// not, since relative paths can't escape the resource's scope.
type communityPathsRule struct{}

func (communityPathsRule) id() string { return "community-paths" }

// kernelStatePrefixes are absolute path roots a community module must
// never touch — kernel/runtime state surfaced by the OS.
var kernelStatePrefixes = []string{"/proc/", "/sys/", "/dev/"}

// perHostPrefixes are absolute path roots that belong to specific
// users or to bootstrapping the machine itself. A community module
// has no portable reason to write here.
var perHostPrefixes = []string{"/root/", "/home/", "/boot/"}

func (communityPathsRule) apply(mod *configcrate, opts Options) []Finding {
	if !opts.CommunityUpload {
		return nil
	}
	var out []Finding
	for _, res := range mod.Resources {
		for _, key := range res.AttrOrder {
			s, ok := res.Attrs[key].(string)
			if !ok || s == "" || s[0] != '/' {
				continue
			}
			if reason := disallowedCommunityPath(s); reason != "" {
				out = append(out, Finding{
					Severity: Error,
					Line:     res.Line,
					Column:   res.Column,
					RuleID:   "community-paths",
					Message: fmt.Sprintf(
						"resource %q attribute %q: %s — community modules cannot touch %q",
						res.Name, key, reason, s,
					),
				})
			}
		}
	}
	return out
}

// disallowedCommunityPath returns a short reason if the absolute path
// `p` is in either blocklist. Empty string means OK.
func disallowedCommunityPath(p string) string {
	for _, pfx := range kernelStatePrefixes {
		if strings.HasPrefix(p, pfx) || p == strings.TrimSuffix(pfx, "/") {
			return "kernel/runtime state path"
		}
	}
	for _, pfx := range perHostPrefixes {
		if strings.HasPrefix(p, pfx) || p == strings.TrimSuffix(pfx, "/") {
			return "per-host territory (user home, root, or boot)"
		}
	}
	return ""
}

func init() { register(communityPathsRule{}) }
