// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import "strings"

// AnnotatePackageSources augments a flattened trait map with a computed
// `approved` field on each `package_source_audit.sources.N.*` row. A
// row's `approved` value is "true" when its URL starts with any of the
// supplied allowlist prefixes, "false" otherwise.
//
// When approvedPrefixes is empty, no annotation is performed — the
// `approved` field is simply absent, which lets the UI distinguish
// "allowlist not configured" from "configured but disapproved" if it
// chooses to.
//
// The allowlist comes from server.yaml's `compliance.approved_sources`.
// Callers resolve it from ConfigStore.ApprovedSources() and pass it in
// to keep fleet/ independent of server/config.
func AnnotatePackageSources(flat map[string]string, approvedPrefixes []string) {
	if len(approvedPrefixes) == 0 {
		return
	}

	const rowPrefix = "package_source_audit.sources."
	const urlSuffix = ".url"

	// Collect (index, url) pairs first so we don't mutate while iterating.
	type pending struct{ index, url string }
	var targets []pending

	for k, v := range flat {
		if !strings.HasPrefix(k, rowPrefix) || !strings.HasSuffix(k, urlSuffix) {
			continue
		}
		inner := k[len(rowPrefix) : len(k)-len(urlSuffix)]
		// Only annotate rows at the expected nesting depth
		// (sources.N.url — a single segment between the prefix and
		// suffix). This guards against nested structures that might
		// accidentally produce a matching key path in the future.
		if strings.Contains(inner, ".") {
			continue
		}
		targets = append(targets, pending{index: inner, url: v})
	}

	for _, t := range targets {
		approvedKey := rowPrefix + t.index + ".approved"
		if urlMatchesAnyPrefix(t.url, approvedPrefixes) {
			flat[approvedKey] = "true"
		} else {
			flat[approvedKey] = "false"
		}
	}
}

// urlMatchesAnyPrefix reports whether url starts with any of the supplied
// prefixes. Simple case-sensitive string-prefix match — package-manager
// URLs are case-preserving and operators paste the exact scheme + host
// they expect, so prefix matching is predictable and requires no regex
// or glob semantics to learn.
func urlMatchesAnyPrefix(url string, prefixes []string) bool {
	for _, p := range prefixes {
		if p == "" {
			continue
		}
		if strings.HasPrefix(url, p) {
			return true
		}
	}
	return false
}
