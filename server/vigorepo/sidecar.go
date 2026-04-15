// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package vigorepo implements the server-side pieces of the vigorepo
// personal-DR subsystem: sidecar validation, the in-memory ClaimTable
// that selects mirror targets, manifest watching, and eviction.
//
// Agents on source envoys discover `.vigorepo.yaml` files under each
// user's `~/vigorepo/*` directory, publish a "meta seed" into the swarm
// containing the literal sidecar bytes, and publish a "content seed"
// containing a `git bundle`. The server subscribes to the swarm
// manifest for `vigorepo-meta:*` labels, fetches each meta payload,
// parses it via [LoadSidecar], and feeds the result into the
// ClaimTable. The ClaimTable runs deterministic selection
// (`blake3(label || hostname)` ascending, first N candidates) and
// ships the per-envoy claim list via `CheckInResponse.vigorepo_claims`.
//
// Server-side sidecar parsing must match the Rust source-side parser
// (`agent/src/vigorepo/sidecar.rs`) exactly: same fields, same strict
// rejection of unknown keys, same clamping of `max_mirrors` to
// [MinMirrors, MaxMirrors], same hostname pattern, same size cap.
// Drift between the two parsers would let a malicious or
// misconfigured source smuggle a sidecar through one side that the
// other rejects. Keep the two in lockstep.
package vigorepo

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// --- Constants (must match agent/src/vigorepo/sidecar.rs and mod.rs) ---

// MinMirrors is the minimum `max_mirrors` value the sidecar may
// request. Values below this are clamped upward with a warning.
const MinMirrors = 3

// MaxMirrors is the maximum `max_mirrors` value the sidecar may
// request. Values above this are clamped downward with a warning.
// Vigorepo is a personal-DR tool; beyond 10 is almost certainly a
// misconfiguration.
const MaxMirrors = 10

// MaxSidecarBytes caps the on-wire size of a `.vigorepo.yaml` file.
// Anything larger is rejected without parsing as a defense against
// YAML bombs.
const MaxSidecarBytes = 8 * 1024

// MaxDescriptionChars caps the length of the `description` field, in
// UTF-8 characters (not bytes).
const MaxDescriptionChars = 256

// MaxTags caps the number of entries in the `tags` list.
const MaxTags = 16

// MaxTagChars caps the length of a single tag string, in UTF-8
// characters.
const MaxTagChars = 32

// MaxHostnameBytes is the RFC 1035 total hostname length cap.
const MaxHostnameBytes = 253

// MaxHostnameLabelBytes is the RFC 1035 per-label hostname length
// cap (a "label" here is a DNS label between dots, not a vigorepo
// swarm label — unfortunate term overlap).
const MaxHostnameLabelBytes = 63

// MaxEnvoyPatterns caps the number of entries in either the
// `include_envoys` or `exclude_envoys` hostname-pattern list. Must
// match Rust [`MAX_ENVOY_PATTERNS`](agent/src/vigorepo/sidecar.rs).
const MaxEnvoyPatterns = 16

// MaxEnvoyPatternBytes caps the length of a single hostname pattern
// entry. Must match Rust [`MAX_ENVOY_PATTERN_BYTES`].
const MaxEnvoyPatternBytes = 253

// MaxWhenExprBytes caps the length of a `when:` expression string.
// Must match Rust [`MAX_WHEN_EXPR_BYTES`].
const MaxWhenExprBytes = 512

// --- Types ---

// Sidecar is the validated, post-clamp contents of a `.vigorepo.yaml`
// file. Five fields are mandatory (owner, max_mirrors, tags, paused,
// description); the candidate-filter fields (include_envoys,
// exclude_envoys, when) are optional and default to empty. This
// struct reflects the parsed state after [LoadSidecar] has run.
type Sidecar struct {
	Owner       string
	MaxMirrors  int
	Tags        []string
	Paused      bool
	Description string
	// IncludeEnvoys is an optional whitelist of hostname glob
	// patterns. When non-empty, a candidate must match at least one
	// entry to be eligible. Patterns are shell-style globs, matched
	// via Go's `path.Match`.
	IncludeEnvoys []string
	// ExcludeEnvoys is an optional blacklist of hostname glob
	// patterns. Candidates matching any entry are dropped. Applied
	// after IncludeEnvoys.
	ExcludeEnvoys []string
	// When is an optional server-side when-expression. Evaluated
	// per-candidate using the same DSL as module `when:` conditions,
	// with a small vigorepo-specific builtin set. Empty string means
	// no filter.
	When string
}

// rawSidecar is the strict YAML deserialization target. The five
// legacy fields stay non-Optional (enforced via non-nil pointer
// checks), so existing sidecars still require them. The filter
// fields are opt-in via pointer-check-absent — omitting them in an
// existing sidecar is equivalent to "no filtering", preserving
// back-compat.
type rawSidecar struct {
	Owner         *string   `yaml:"owner"`
	MaxMirrors    *int64    `yaml:"max_mirrors"`
	Tags          *[]string `yaml:"tags"`
	Paused        *bool     `yaml:"paused"`
	Description   *string   `yaml:"description"`
	IncludeEnvoys *[]string `yaml:"include_envoys"`
	ExcludeEnvoys *[]string `yaml:"exclude_envoys"`
	When          *string   `yaml:"when"`
}

// --- Errors ---

// SidecarError describes a specific rejection reason. Keep the set
// aligned with the Rust `SidecarError` enum in `sidecar.rs` so
// cross-language log searches can match on the same text.
type SidecarError struct {
	Kind    string // short error kind, stable identifier
	Message string // human-readable detail
}

func (e *SidecarError) Error() string { return e.Message }

func errKind(kind, msg string) *SidecarError {
	return &SidecarError{Kind: kind, Message: msg}
}

// Sentinel errors for common failure modes.
var (
	ErrTooLarge       = errKind("too_large", "sidecar exceeds size cap")
	ErrMissingField   = errKind("missing_field", "sidecar missing required field")
	ErrUnknownField   = errKind("unknown_field", "sidecar contains unknown field")
	ErrInvalidOwner   = errKind("invalid_owner", "sidecar owner is not a valid hostname")
	ErrInvalidMirror  = errKind("invalid_mirrors", "sidecar max_mirrors is not a non-negative integer")
	ErrTooLongDesc    = errKind("description_too_long", "sidecar description exceeds character limit")
	ErrTooManyTags    = errKind("too_many_tags", "sidecar tags list exceeds cap")
	ErrInvalidTag     = errKind("invalid_tag", "sidecar tag is empty or exceeds character limit")
	ErrParse          = errKind("parse", "sidecar YAML parse error")
	ErrTooManyPats    = errKind("too_many_patterns", "sidecar envoy-pattern list exceeds cap")
	ErrInvalidPattern = errKind("invalid_pattern", "sidecar envoy pattern is empty or exceeds byte limit")
	ErrWhenTooLong    = errKind("when_too_long", "sidecar when expression exceeds byte limit")
)

// --- Parse ---

// LoadSidecar parses and validates a `.vigorepo.yaml` byte payload.
// The byte cap is enforced *before* parsing so a hostile input
// cannot exhaust memory via a YAML bomb. `max_mirrors` is clamped
// with a warning flag — the returned sidecar always has a value in
// `[MinMirrors, MaxMirrors]`. If `clamped` is true on return, the
// caller should surface the original requested value to operators
// so they can fix the sidecar upstream.
//
// This function must accept exactly the same inputs as
// `agent/src/vigorepo/sidecar.rs::parse_str`. When changing either
// side, change both — drift between them is a correctness bug, not
// a cosmetic mismatch.
func LoadSidecar(data []byte) (sidecar *Sidecar, clamped bool, err error) {
	if len(data) > MaxSidecarBytes {
		return nil, false, &SidecarError{
			Kind:    "too_large",
			Message: fmt.Sprintf("sidecar exceeds %d byte cap (%d bytes)", MaxSidecarBytes, len(data)),
		}
	}

	var raw rawSidecar
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		// yaml.v3 reports unknown fields as "field X not found in
		// type". Classify best-effort so the error kind is stable
		// across runs; always include the original text for the
		// operator.
		msg := err.Error()
		if containsField(msg, "not found in type") {
			return nil, false, &SidecarError{Kind: "unknown_field", Message: msg}
		}
		return nil, false, &SidecarError{Kind: "parse", Message: msg}
	}

	// yaml.v3 does not emit its own "required" concept; we enforce
	// required-ness via non-nil pointer fields and check here. The
	// order matches the Rust parser's error precedence so logs and
	// tests stay aligned.
	if raw.Owner == nil {
		return nil, false, &SidecarError{Kind: "missing_field", Message: "sidecar missing required field: owner"}
	}
	if raw.MaxMirrors == nil {
		return nil, false, &SidecarError{Kind: "missing_field", Message: "sidecar missing required field: max_mirrors"}
	}
	if raw.Tags == nil {
		return nil, false, &SidecarError{Kind: "missing_field", Message: "sidecar missing required field: tags"}
	}
	if raw.Paused == nil {
		return nil, false, &SidecarError{Kind: "missing_field", Message: "sidecar missing required field: paused"}
	}
	if raw.Description == nil {
		return nil, false, &SidecarError{Kind: "missing_field", Message: "sidecar missing required field: description"}
	}

	// Owner hostname validation. Lowercased before validation to
	// match the case-insensitive discovery check in
	// `agent/src/vigorepo/source.rs`.
	owner := lowerASCII(*raw.Owner)
	if !isValidHostname(owner) {
		return nil, false, &SidecarError{
			Kind:    "invalid_owner",
			Message: fmt.Sprintf("sidecar owner field '%s' is not a valid hostname", *raw.Owner),
		}
	}

	// max_mirrors: reject negative, clamp positive into range.
	if *raw.MaxMirrors < 0 {
		return nil, false, &SidecarError{
			Kind:    "invalid_mirrors",
			Message: fmt.Sprintf("sidecar max_mirrors %d is negative", *raw.MaxMirrors),
		}
	}
	requested := int(*raw.MaxMirrors)
	clampedMirrors := requested
	if clampedMirrors < MinMirrors {
		clampedMirrors = MinMirrors
		clamped = true
	}
	if clampedMirrors > MaxMirrors {
		clampedMirrors = MaxMirrors
		clamped = true
	}

	// Tags: cap count + per-tag length, reject empty entries.
	tags := *raw.Tags
	if len(tags) > MaxTags {
		return nil, false, &SidecarError{
			Kind:    "too_many_tags",
			Message: fmt.Sprintf("sidecar has %d tags (maximum %d)", len(tags), MaxTags),
		}
	}
	for _, t := range tags {
		if len(t) == 0 || utf8CharCount(t) > MaxTagChars {
			return nil, false, &SidecarError{
				Kind: "invalid_tag",
				Message: fmt.Sprintf(
					"sidecar tag '%s' is empty or exceeds %d character limit",
					t, MaxTagChars),
			}
		}
	}

	// Description: cap char count (not bytes).
	if utf8CharCount(*raw.Description) > MaxDescriptionChars {
		return nil, false, &SidecarError{
			Kind: "description_too_long",
			Message: fmt.Sprintf(
				"sidecar description exceeds %d character limit (%d chars)",
				MaxDescriptionChars, utf8CharCount(*raw.Description)),
		}
	}

	// include_envoys / exclude_envoys — optional; when present, bound
	// count and per-entry length. Parity with the Rust source-side
	// parser is load-bearing: the cross-language parity test refuses
	// drift between these two validators.
	var includeEnvoys, excludeEnvoys []string
	if raw.IncludeEnvoys != nil {
		if err := validatePatternList("include_envoys", *raw.IncludeEnvoys); err != nil {
			return nil, false, err
		}
		includeEnvoys = *raw.IncludeEnvoys
	}
	if raw.ExcludeEnvoys != nil {
		if err := validatePatternList("exclude_envoys", *raw.ExcludeEnvoys); err != nil {
			return nil, false, err
		}
		excludeEnvoys = *raw.ExcludeEnvoys
	}

	// when — optional; bound the expression length. Parsing and
	// per-candidate evaluation are server-side concerns handled by
	// the ClaimTable filter pipeline. Empty means no filter.
	var whenExpr string
	if raw.When != nil {
		if len(*raw.When) > MaxWhenExprBytes {
			return nil, false, &SidecarError{
				Kind: "when_too_long",
				Message: fmt.Sprintf(
					"sidecar when expression exceeds %d byte limit (%d bytes)",
					MaxWhenExprBytes, len(*raw.When)),
			}
		}
		whenExpr = *raw.When
	}

	return &Sidecar{
		Owner:         owner,
		MaxMirrors:    clampedMirrors,
		Tags:          tags,
		Paused:        *raw.Paused,
		Description:   *raw.Description,
		IncludeEnvoys: includeEnvoys,
		ExcludeEnvoys: excludeEnvoys,
		When:          whenExpr,
	}, clamped, nil
}

// validatePatternList enforces the `include_envoys`/`exclude_envoys`
// shape constraints. Each entry must be non-empty and ≤
// [MaxEnvoyPatternBytes] bytes; the whole list must have ≤
// [MaxEnvoyPatterns] entries. Pattern syntax itself is not checked —
// unmatchable globs simply match nothing at selection time.
func validatePatternList(field string, patterns []string) *SidecarError {
	if len(patterns) > MaxEnvoyPatterns {
		return &SidecarError{
			Kind: "too_many_patterns",
			Message: fmt.Sprintf(
				"sidecar %s has %d entries (maximum %d)",
				field, len(patterns), MaxEnvoyPatterns),
		}
	}
	for _, p := range patterns {
		if p == "" || len(p) > MaxEnvoyPatternBytes {
			return &SidecarError{
				Kind: "invalid_pattern",
				Message: fmt.Sprintf(
					"sidecar %s pattern '%s' is empty or exceeds %d byte limit",
					field, p, MaxEnvoyPatternBytes),
			}
		}
	}
	return nil
}

// --- Helpers ---

// isValidHostname matches the Rust source-side hostname validator in
// `agent/src/vigorepo/sidecar.rs::is_valid_hostname`. Pattern:
//
//	[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)*
//
// Callers must lowercase the input first.
func isValidHostname(s string) bool {
	if s == "" || len(s) > MaxHostnameBytes {
		return false
	}
	for _, label := range splitDot(s) {
		if label == "" || len(label) > MaxHostnameLabelBytes {
			return false
		}
		first := label[0]
		if !isLowerAlphaNum(first) {
			return false
		}
		for i := 1; i < len(label); i++ {
			c := label[i]
			if !isLowerAlphaNum(c) && c != '-' {
				return false
			}
		}
	}
	return true
}

func isLowerAlphaNum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// splitDot splits on '.' without allocating an `strings.Split` slice
// for tiny inputs — this runs in the check-in hot path.
func splitDot(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// lowerASCII lowercases ASCII letters in place. Non-ASCII input
// passes through unchanged (hostnames per `isValidHostname` are
// ASCII-only anyway, so non-ASCII input will be rejected by the
// validator one call later).
func lowerASCII(s string) string {
	buf := []byte(s)
	for i := 0; i < len(buf); i++ {
		if buf[i] >= 'A' && buf[i] <= 'Z' {
			buf[i] += 'a' - 'A'
		}
	}
	return string(buf)
}

// utf8CharCount returns the number of UTF-8 code points in s,
// matching Rust's `str::chars().count()` semantics.
func utf8CharCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// containsField is a tiny helper for classifying yaml.v3 error
// strings without pulling in regexp.
func containsField(haystack, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}
