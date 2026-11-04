// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package validate

import "strings"

// identityKeys lists, per resource kind, the attribute keys that name the
// thing a resource manages, canonical key first then accepted aliases (see
// the executor schemas in schemas_gen.go). The self-protection checks and
// the retract generator (server/publish/retract.go) both resolve a
// resource's target through IdentityAttr, so they can never disagree about
// which attribute a resource uses.
var identityKeys = map[string][]string{
	"path":       {"target_path", "path"},
	"service":    {"service"},
	"package":    {"package"},
	"user":       {"username"},
	"repository": {"repository", "repo"},
	"sysctl":     {"key"},
	"exec":       {"command", "cmd"},
}

// identityKind maps a resource type to its identityKeys entry.
func identityKind(resType string) string {
	switch {
	case resType == "file", resType == "directory", resType == "symlink", resType == "file_line",
		resType == "blockinfile", resType == "ini", resType == "json_file", resType == "replace",
		resType == "field_edit", resType == "source_package":
		return "path"
	case resType == "service" || strings.HasPrefix(resType, "service_"):
		return "service"
	case resType == "user" || strings.HasPrefix(resType, "user_"):
		return "user"
	case resType == "sysctl" || resType == "sysctl_bsd":
		return "sysctl"
	}
	return resType
}

// IdentityAttr returns the attribute key and value that identify what a
// resource of resType manages, or ("", "") when it carries none of the
// accepted keys.
func IdentityAttr(resType string, attrs map[string]any) (key, value string) {
	for _, k := range identityKeys[identityKind(resType)] {
		if v := attrString(attrs, k); v != "" {
			return k, v
		}
	}
	return "", ""
}
