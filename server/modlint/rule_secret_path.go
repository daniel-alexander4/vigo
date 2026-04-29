// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"
	"regexp"
	"strings"
)

// secretPathRule validates the shape of `secret:`-prefixed attribute
// values.
//
// The convention is `secret:<path>` where path is lowercase, slash-
// separated, ASCII alphanumerics + `_`/`-`/`.`. A malformed path
// (uppercase, spaces, control characters, empty) lints clean today
// and explodes at secret-resolution time — by which point the
// publish has already shipped the broken module to the fleet.
//
// Examples:
//
//	secret:vigo/db/dsn         OK
//	secret:vigo/auth/admin     OK
//	secret:                    REJECTED (empty)
//	secret:Foo/Bar             REJECTED (uppercase)
//	secret:foo bar/baz         REJECTED (whitespace)
//	secret:..                  REJECTED (path traversal shape)
//
// Doesn't fire in CommunityUpload mode — communityNoSecretsRule has
// already rejected the whole module by then.
var secretPathPattern = regexp.MustCompile(`^secret:[a-z0-9_-]+(/[a-z0-9_.-]+)*$`)

type secretPathRule struct{}

func (secretPathRule) id() string { return "secret-path" }

func (secretPathRule) apply(mod *module, opts Options) []Finding {
	if opts.CommunityUpload {
		return nil
	}
	var out []Finding
	for _, res := range mod.Resources {
		for _, key := range res.AttrOrder {
			s, ok := res.Attrs[key].(string)
			if !ok || !strings.HasPrefix(s, "secret:") {
				continue
			}
			if secretPathPattern.MatchString(s) {
				continue
			}
			out = append(out, Finding{
				Severity: Error,
				Line:     res.Line,
				Column:   res.Column,
				RuleID:   "secret-path",
				Message: fmt.Sprintf(
					"resource %q attribute %q: %q is not a valid secret reference (expected secret:<lowercase path with /, _, -, .>)",
					res.Name, key, s,
				),
			})
		}
	}
	return out
}

func init() { register(secretPathRule{}) }
