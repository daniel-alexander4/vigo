// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package fleet

import "testing"

func TestAnnotatePackageSources_NoAllowlist(t *testing.T) {
	flat := map[string]string{
		"package_source_audit.sources.0.url":  "http://archive.ubuntu.com/ubuntu",
		"package_source_audit.sources.0.type": "apt",
	}
	AnnotatePackageSources(flat, nil)
	if _, ok := flat["package_source_audit.sources.0.approved"]; ok {
		t.Errorf("expected no approved key when allowlist is empty, got %v", flat)
	}
}

func TestAnnotatePackageSources_ApprovedAndDisapproved(t *testing.T) {
	flat := map[string]string{
		"package_source_audit.sources.0.url":  "http://archive.ubuntu.com/ubuntu",
		"package_source_audit.sources.0.type": "apt",
		"package_source_audit.sources.1.url":  "https://rogue.example.com/repo",
		"package_source_audit.sources.1.type": "apt",
		"package_source_audit.sources.2.url":  "https://download.docker.com/linux/centos",
		"package_source_audit.sources.2.type": "yum",
	}

	AnnotatePackageSources(flat, []string{
		"http://archive.ubuntu.com/",
		"https://download.docker.com/",
	})

	if flat["package_source_audit.sources.0.approved"] != "true" {
		t.Errorf("sources.0 should be approved, got %q", flat["package_source_audit.sources.0.approved"])
	}
	if flat["package_source_audit.sources.1.approved"] != "false" {
		t.Errorf("sources.1 should be disapproved, got %q", flat["package_source_audit.sources.1.approved"])
	}
	if flat["package_source_audit.sources.2.approved"] != "true" {
		t.Errorf("sources.2 should be approved, got %q", flat["package_source_audit.sources.2.approved"])
	}
}

func TestAnnotatePackageSources_IgnoresOtherTraits(t *testing.T) {
	flat := map[string]string{
		"security_scan.cves.0.id":  "CVE-2024-1234",
		"security_scan.cves.0.url": "https://nvd.nist.gov/vuln/detail/CVE-2024-1234",
	}
	AnnotatePackageSources(flat, []string{"https://"})
	if _, ok := flat["security_scan.cves.0.approved"]; ok {
		t.Errorf("should not annotate security_scan rows, got %v", flat)
	}
}

func TestAnnotatePackageSources_IgnoresNestedUrls(t *testing.T) {
	// A future trait that happened to use package_source_audit.sources.0.mirror.url
	// should NOT be annotated — only the flat sources.N.url shape is the
	// target.
	flat := map[string]string{
		"package_source_audit.sources.0.mirror.url": "http://archive.ubuntu.com/",
	}
	AnnotatePackageSources(flat, []string{"http://"})
	if _, ok := flat["package_source_audit.sources.0.mirror.approved"]; ok {
		t.Errorf("should not annotate nested .url fields, got %v", flat)
	}
}

func TestAnnotatePackageSources_EmptyPrefixSkipped(t *testing.T) {
	// An empty-string prefix would HasPrefix-match everything; guard
	// against it explicitly so a stray empty entry doesn't silently
	// approve all sources.
	flat := map[string]string{
		"package_source_audit.sources.0.url": "https://rogue.example.com/",
	}
	AnnotatePackageSources(flat, []string{""})
	if flat["package_source_audit.sources.0.approved"] != "false" {
		t.Errorf("empty-prefix entry should not approve rogue URL, got %q",
			flat["package_source_audit.sources.0.approved"])
	}
}
