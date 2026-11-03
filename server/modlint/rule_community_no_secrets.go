// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package modlint

import (
	"fmt"
	"strings"
)

// communityNoSecretsRule rejects any `secret:`-prefixed attribute value
// in modules submitted via the community-upload path.
//
// Community modules are intended to be portable — they install software
// or configure a generic system role on any operator's fleet. A
// `secret:` reference is by definition operator-local (it resolves
// against THAT operator's secrets store), so a community module
// embedding one would either fail to load on every other fleet or
// expose the path layout of the original author's secrets tree. Reject
// at upload time so neither happens.
//
// Only fires when opts.CommunityUpload is true; in-fleet modules can
// reference secrets freely.
type communityNoSecretsRule struct{}

func (communityNoSecretsRule) id() string { return "community-no-secrets" }

func (communityNoSecretsRule) apply(mod *configcrate, opts Options) []Finding {
	if !opts.CommunityUpload {
		return nil
	}
	var out []Finding
	for _, res := range mod.Resources {
		for _, key := range res.AttrOrder {
			s, ok := res.Attrs[key].(string)
			if !ok || !strings.HasPrefix(s, "secret:") {
				continue
			}
			out = append(out, Finding{
				Severity: Error,
				Line:     res.Line,
				Column:   res.Column,
				RuleID:   "community-no-secrets",
				Message: fmt.Sprintf(
					"resource %q: community modules cannot use secret: references (attribute %q)",
					res.Name, key,
				),
			})
		}
	}
	return out
}

func init() { register(communityNoSecretsRule{}) }
