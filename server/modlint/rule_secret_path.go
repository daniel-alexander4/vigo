// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package modlint

import (
	"fmt"
	"sort"

	"git.alexander4.io/vigo/server/secretref"
)

// secretPathRule validates every `secret:` reference in a configcrate —
// in resource attributes and vars, whole-value or embedded, at any depth
// of a list or map value — against secretref.ValidateKey, the grammar the
// config loader and the local secrets backend also enforce. A malformed
// key (uppercase, spaces, `..`, a hidden name, empty) would otherwise lint
// clean and only fail at secret-resolution time.
//
// Examples:
//
//	secret:vigo/db/dsn         OK
//	PW=secret:vigo/db/pw,X=1   OK (embedded)
//	secret:                    REJECTED (empty)
//	secret:Foo/Bar             REJECTED (uppercase)
//	secret:vigo/../x           REJECTED (path traversal)
//	secret:.master.key         REJECTED (hidden name)
//
// Doesn't fire in CommunityUpload mode — communityNoSecretsRule has
// already rejected the whole module by then.
type secretPathRule struct{}

func (secretPathRule) id() string { return "secret-path" }

func (secretPathRule) apply(mod *configcrate, opts Options) []Finding {
	if opts.CommunityUpload {
		return nil
	}
	var out []Finding
	report := func(where string, line, col int, v any) {
		walkStrings(v, func(s string) {
			for _, sp := range secretref.Scan(s) {
				if err := secretref.ValidateKey(sp.Key); err != nil {
					out = append(out, Finding{
						Severity: Error,
						Line:     line,
						Column:   col,
						RuleID:   "secret-path",
						Message:  fmt.Sprintf("%s: %q is not a valid secret reference: %v", where, s[sp.Start:sp.End], err),
					})
				}
			}
		})
	}
	varKeys := make([]string, 0, len(mod.Vars))
	for k := range mod.Vars {
		varKeys = append(varKeys, k)
	}
	sort.Strings(varKeys)
	for _, k := range varKeys {
		report(fmt.Sprintf("var %q", k), 1, 1, mod.Vars[k])
	}
	for _, res := range mod.Resources {
		for _, key := range res.AttrOrder {
			report(fmt.Sprintf("resource %q attribute %q", res.Name, key), res.Line, res.Column, res.Attrs[key])
		}
	}
	return out
}

// walkStrings calls fn on every string reachable from v through lists and
// maps.
func walkStrings(v any, fn func(string)) {
	switch t := v.(type) {
	case string:
		fn(t)
	case []any:
		for _, item := range t {
			walkStrings(item, fn)
		}
	case map[string]any:
		for _, item := range t {
			walkStrings(item, fn)
		}
	case map[any]any:
		for _, item := range t {
			walkStrings(item, fn)
		}
	}
}

func init() { register(secretPathRule{}) }
