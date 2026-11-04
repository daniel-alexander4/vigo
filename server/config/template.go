// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"text/template"
)

// templateContext is the context passed to Go templates when rendering
// resource attributes, module vars, etc.
type templateContext struct {
	Traits map[string]any
	Vars   map[string]any
}

// RenderTemplate renders a Go template string with .Traits and .Vars namespaces.
// Templates are rendered at check-in time (not at config load time) so that
// traits from the requesting agent are available.
func RenderTemplate(tmpl string, traits map[string]any, vars map[string]any) (string, error) {
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}
	var buf bytes.Buffer
	ctx := templateContext{
		Traits: traits,
		Vars:   vars,
	}
	if err := t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}
	return buf.String(), nil
}

// traitValue looks up a trait by dotted key in a traits map that may be flat
// ("os.family" keys, agent check-in format) or nested ({"os": {"family": ...}}).
func traitValue(traits map[string]any, key string) string {
	if v, ok := traits[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	parts := strings.SplitN(key, ".", 2)
	if len(parts) == 2 {
		if sub, ok := traits[parts[0]].(map[string]any); ok {
			if s, ok := sub[parts[1]].(string); ok {
				return s
			}
		}
	}
	return ""
}

// evalTraitFunc evaluates a trait-based builtin (distro/os_family/os/arch/
// version_ge/has_display) against a traits map. Unknown function names → false.
// Shared by when:-expression evaluation and fleet: selector evaluation.
func evalTraitFunc(funcName, argStr string, traits map[string]any) bool {
	switch funcName {
	case "distro":
		return strings.EqualFold(traitValue(traits, "os.distro"), argStr)
	case "os_family":
		// "linux" matches any Linux-based family (debian, rhel, suse, etc.)
		if strings.EqualFold(argStr, "linux") {
			family := strings.ToLower(traitValue(traits, "os.family"))
			return family != "" && family != "windows" && family != "darwin" &&
				family != "freebsd" && family != "openbsd" && family != "netbsd" && family != "illumos"
		}
		return strings.EqualFold(traitValue(traits, "os.family"), argStr)
	case "os":
		return strings.EqualFold(traitValue(traits, "os.distro"), argStr)
	case "arch":
		return strings.EqualFold(traitValue(traits, "os.arch"), argStr)
	case "version_ge":
		return versionGE(traitValue(traits, "os.version"), argStr)
	case "has_display":
		return traitValue(traits, "display.server") != "" && traitValue(traits, "display.server") != "none"
	}
	return false
}

// versionGE reports whether version string a is >= b, comparing dotted
// numeric components rather than lexically ("9" >= "10" must be false).
// Each component is parsed up to its first non-digit, so a platform suffix
// like FreeBSD's "14.0-RELEASE" reads as (14, 0); a missing component is
// treated as 0 ("9" == "9.0", "9" < "9.5"). Inputs with no leading digits
// compare as 0. OS version strings never carry semver pre-release/build
// metadata, so a full semver parser would be over-built here.
func versionGE(a, b string) bool {
	return compareVersions(a, b) >= 0
}

func compareVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(as) {
			av = leadingInt(as[i])
		}
		if i < len(bs) {
			bv = leadingInt(bs[i])
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// leadingInt parses the leading run of digits in s, returning 0 if there are
// none (tolerates suffixes like "-RELEASE" on a version component).
func leadingInt(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}
