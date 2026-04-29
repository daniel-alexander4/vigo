// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"
	"strconv"
	"strings"
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

		// The int alone is ambiguous because yaml.v3 parses leading-zero
		// scalars as octal (YAML 1.1 carryover), so `mode: 0644` arrives
		// as int 420 while `mode: 644` arrives as int 644 — both intend
		// rw-r--r--. Recover the operator's intent from the raw YAML text.
		var origText string
		if node, ok := res.AttrNodes["mode"]; ok && node != nil {
			origText = node.Value
		}

		// Choose the digit-extraction strategy based on what the operator
		// actually typed:
		//   - "0o644" / "0O644" → explicit YAML 1.2 octal; int already
		//     equals the octal value, so format it back in base 8.
		//   - "0644" (leading zero followed by digits) → yaml.v3's YAML
		//     1.1 octal carryover; same path as above.
		//   - "644" (bare digits, no leading zero) → operator typed the
		//     octal-positional digits as a decimal int; render in base 10
		//     and treat each digit as octal (the original 0.33.6 logic).
		//   - missing node (defensive) → bare-digits path.
		var digits string
		switch {
		case strings.HasPrefix(origText, "0o") || strings.HasPrefix(origText, "0O"):
			digits = strconv.FormatInt(n, 8)
		case len(origText) >= 2 && origText[0] == '0' && isAllDigits(origText):
			digits = strconv.FormatInt(n, 8)
		default:
			digits = strconv.FormatInt(n, 10)
		}
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
		typed := origText
		if typed == "" {
			typed = strconv.FormatInt(n, 10)
		}
		out = append(out, Finding{
			Severity:   Warning,
			Line:       res.Line,
			Column:     res.Column,
			RuleID:     "mode-octal",
			FixApplied: true,
			Message: fmt.Sprintf(
				"resource %q: mode was unquoted %s — auto-quoted to %q (YAML lacks an unambiguous octal literal; without quoting the agent applies the wrong permissions)",
				res.Name, typed, fixed,
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
