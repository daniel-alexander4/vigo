// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"
	"strconv"
)

// modeOctalRule auto-fixes the YAML-octal-vs-decimal trap.
//
// YAML 1.2 has no octal literal syntax. A user writing `mode: 644`
// intends rw-r--r-- (octal 0644 = decimal 420), but the parser produces
// the integer 644. Without intervention, the canonical validator
// reformats that integer back to "1204" (the octal representation of
// 644 decimal) and the agent applies mode 0o1204 — wrong permissions
// silently shipped to the fleet.
//
// Detection: `mode` attribute parsed as an integer. Auto-fix: convert
// to a zero-padded 4-digit string of the integer's *digits* (treating
// the operator's typed value as if it had been quoted), provided every
// decimal digit is in [0-7]. So `644` → `"0644"`, `755` → `"0755"`,
// `4755` → `"4755"`. Integers with a digit > 7 (e.g., `888`) can't be
// octal under any interpretation; emit a non-fix error.
//
// Strings already in the file (`mode: '0644'`) are pass-through.
// Validate-adapter still catches malformed strings via its own
// requireValidMode check.
type modeOctalRule struct{}

func (modeOctalRule) id() string { return "mode-octal" }

func (modeOctalRule) apply(mod *module, _ Options) []Finding {
	var out []Finding
	for i := range mod.Resources {
		res := &mod.Resources[i]
		raw, ok := res.Attrs["mode"]
		if !ok {
			continue
		}
		// Strings (already quoted) are someone else's problem.
		if _, isStr := raw.(string); isStr {
			continue
		}
		var n int64
		switch t := raw.(type) {
		case int:
			n = int64(t)
		case int64:
			n = t
		default:
			continue
		}

		// User-typed digits: render the decimal representation as digits
		// and validate each is 0-7. This treats the value as if the user
		// had quoted it from the start.
		digits := strconv.FormatInt(n, 10)
		if !allOctalDigits(digits) {
			out = append(out, Finding{
				Severity: Error,
				Line:     res.Line,
				Column:   res.Column,
				RuleID:   "mode-octal",
				Message: fmt.Sprintf(
					"resource %q: mode %d contains non-octal digits — must be 3-4 digits each in 0-7 (e.g. \"0644\")",
					res.Name, n,
				),
			})
			continue
		}
		if len(digits) < 3 || len(digits) > 4 {
			out = append(out, Finding{
				Severity: Error,
				Line:     res.Line,
				Column:   res.Column,
				RuleID:   "mode-octal",
				Message: fmt.Sprintf(
					"resource %q: mode %d is %d digits — must be 3 or 4",
					res.Name, n, len(digits),
				),
			})
			continue
		}
		// Auto-fix: pad to 4 digits with a leading zero and quote as a string.
		fixed := digits
		if len(fixed) == 3 {
			fixed = "0" + fixed
		}
		res.Attrs["mode"] = fixed
		// Drop the AttrNode for mode so render falls back to the
		// fixed string value rather than re-emitting the original
		// bare-integer node.
		delete(res.AttrNodes, "mode")
		out = append(out, Finding{
			Severity:   Warning,
			Line:       res.Line,
			Column:     res.Column,
			RuleID:     "mode-octal",
			FixApplied: true,
			Message: fmt.Sprintf(
				"resource %q: mode was bare integer %d — auto-quoted to %q (YAML has no octal literal; without quoting the agent applies the wrong permissions)",
				res.Name, n, fixed,
			),
		})
	}
	return out
}

func allOctalDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '7' {
			return false
		}
	}
	return s != ""
}

func init() { register(modeOctalRule{}) }
